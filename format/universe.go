package format

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/checker"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
)

// Universe is a set of packages checked together: every package's dependencies must be in it.
type Universe struct {
	Packages map[string]*Package
	// Kernel names the package that is the kernel: the one whose definitions verification
	// interprets. The interpretation record names it (its kernel key), and nothing else does:
	// the format reads the kernel's name from here, never from a constant. A universe without it
	// has no kernel, so no package depends on one.
	Kernel string
	errs   []string
	decls  map[string]*Decl // object types in expressions, by name
	base   *cel.Env         // pure library
	state  *cel.Env         // pure and state library
	used   map[string]bool  // "package|dependency" for each dependency a package names
}

// Option sets what a universe cannot be told from its packages.
type Option func(*Universe)

// Kernel names the package that is the kernel, as the interpretation record's kernel key does.
func Kernel(name string) Option { return func(u *Universe) { u.Kernel = name } }

// Check checks a set of packages against the format and returns every problem, sorted.
func Check(pkgs map[string]*Package, opts ...Option) []string {
	_, errs := Prepare(pkgs, opts...)
	return errs
}

// Prepare checks a set of packages against the format, and returns the universe their
// expressions compile and evaluate in, with every problem, sorted.
func Prepare(pkgs map[string]*Package, opts ...Option) (*Universe, []string) {
	u := &Universe{Packages: pkgs, decls: map[string]*Decl{}, used: map[string]bool{}}
	for _, o := range opts {
		o(u)
	}
	for _, name := range sortedKeys(pkgs) {
		u.errs = append(u.errs, pkgs[name].Errors()...)
	}
	u.dependencies()
	u.kernelContext()
	u.resolveAll()
	if err := u.environments(); err != nil {
		u.errs = append(u.errs, "building the expression environment: "+err.Error())
		return u, u.errs
	}
	for _, name := range sortedKeys(pkgs) {
		p := pkgs[name]
		for _, dname := range sortedKeys(p.Defs) {
			u.checkDefinition(p, p.Defs[dname])
		}
	}
	u.unusedDependencies()
	sort.Strings(u.errs)
	return u, append([]string(nil), u.errs...)
}

// The definitions of the kernel package that the expression variables custody and trust_root are
// read from.
const (
	custodyDefinition   = "custody-profile"
	trustRootDefinition = "trust-root"
)

// contextMissing says whether the kernel package lacks a definition the variables custody and
// trust_root are read from, while an expression of a package that reads the kernel's context
// names one of the variables. Without such an expression nothing is left unchecked.
func (u *Universe) contextMissing() bool {
	kernel := u.Packages[u.Kernel]
	if kernel == nil || (kernel.Defs[custodyDefinition] != nil && kernel.Defs[trustRootDefinition] != nil) {
		return false
	}
	for _, p := range u.Packages {
		if (p == kernel || contains(p.DependsOn, u.Kernel)) && namesContext(p.Content) {
			return true
		}
	}
	return false
}

var contextVariable = regexp.MustCompile(`(^|[^\w.])(custody|trust_root)\.`)

// namesContext says whether a string anywhere in a package's content names custody or trust_root
// as a variable, as an expression that reads them does.
func namesContext(v any) bool {
	switch x := v.(type) {
	case string:
		return contextVariable.MatchString(x)
	case map[string]any:
		for _, e := range x {
			if namesContext(e) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if namesContext(e) {
				return true
			}
		}
	}
	return false
}

// kernelContext reports, once, a kernel that lacks a definition the format derives its context
// variables from, so that a renamed definition shows as one cause and not as an undeclared
// reference in every expression that reads custody or trust_root.
func (u *Universe) kernelContext() {
	if !u.contextMissing() {
		return
	}
	kernel := u.Packages[u.Kernel]
	for _, v := range []struct{ variable, definition string }{{"custody", custodyDefinition}, {"trust_root", trustRootDefinition}} {
		if kernel.Defs[v.definition] == nil {
			u.errorf(kernel, "", "the format derives %s from the definition %s of the kernel package, which does not exist; every expression that reads %s is left unchecked", v.variable, v.definition, v.variable)
		}
	}
}

// unusedDependencies reports a dependency that nothing in its package names, since it would pull
// a package into every version that lists the dependent. The kernel is exempt: depending on it
// brings its context variables into every expression.
func (u *Universe) unusedDependencies() {
	for _, name := range sortedKeys(u.Packages) {
		p := u.Packages[name]
		for _, d := range p.DependsOn {
			if d != u.Kernel && u.Packages[d] != nil && !u.used[name+"|"+d] {
				u.errorf(p, "depends_on", "lists package %s, which nothing in the package names", d)
			}
		}
	}
}

// checkDefinition checks one definition, reporting a failure of the checker itself as an error
// that names the definition instead of stopping the check of every other.
func (u *Universe) checkDefinition(p *Package, d *Definition) {
	defer func() {
		if r := recover(); r != nil {
			u.errorf(p, d.Name, "internal error in the checker while checking this definition: %v", r)
		}
	}()
	u.definition(p, d)
}

func (u *Universe) errorf(p *Package, where, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if where != "" {
		msg = where + ": " + msg
	}
	msg = p.File + ": " + msg
	if !contains(u.errs, msg) { // a macro that reads its target twice would report what it holds twice
		u.errs = append(u.errs, msg)
	}
}

func (u *Universe) dependencies() {
	for _, name := range sortedKeys(u.Packages) {
		p := u.Packages[name]
		for _, d := range p.DependsOn {
			if u.Packages[d] == nil {
				u.errorf(p, "depends_on", "package %s is not loaded with it", d)
			}
		}
	}
	state := map[string]int{} // 1 visiting, 2 done
	var visit func(string, []string)
	visit = func(n string, trail []string) {
		switch state[n] {
		case 1:
			u.errorf(u.Packages[trail[0]], "depends_on", "dependencies form a cycle: %s", strings.Join(append(trail, n), " -> "))
			return
		case 2:
			return
		}
		state[n] = 1
		if p := u.Packages[n]; p != nil {
			for _, d := range p.DependsOn {
				visit(d, append(trail, n))
			}
		}
		state[n] = 2
	}
	for _, name := range sortedKeys(u.Packages) {
		visit(name, nil)
	}
}

// --- types ---

// builtinTypesOnce parses the field types of the built-in records once, so checks that run at the same time share nothing they change.
var builtinTypesOnce sync.Once

func (u *Universe) resolveAll() {
	builtinTypesOnce.Do(func() {
		for _, b := range builtinRecords {
			for _, f := range b.Fields {
				f.Type, _ = ParseType(f.TypeSrc)
			}
		}
	})
	for _, name := range sortedKeys(builtinRecords) {
		b := builtinRecords[name]
		u.decls[b.CELName()] = b
	}
	for _, name := range sortedKeys(u.Packages) {
		p := u.Packages[name]
		for _, dname := range sortedKeys(p.Defs) {
			u.register(p, p.Defs[dname].Decl, dname)
		}
	}
	for _, name := range sortedKeys(u.Packages) {
		p := u.Packages[name]
		for _, tname := range sortedKeys(p.Types) {
			u.resolveDecl(p, nil, p.Types[tname], "types."+tname)
		}
		for _, dname := range sortedKeys(p.Defs) {
			d := p.Defs[dname]
			u.resolveDecl(p, d, d.Decl, dname+".structure")
			for _, tname := range sortedKeys(d.Types) {
				u.resolveDecl(p, d, d.Types[tname], dname+".types."+tname)
			}
			for _, oname := range sortedKeys(d.Inputs) {
				u.resolveDecl(p, d, d.Inputs[oname], dname+".operations."+oname+".inputs")
			}
			for _, oname := range sortedKeys(d.Outputs) {
				u.resolveDecl(p, d, d.Outputs[oname], dname+".operations."+oname+".outputs")
			}
		}
	}
}

// register gives a type its name in expressions, refusing a name already taken.
func (u *Universe) register(p *Package, decl *Decl, where string) {
	if prior, dup := u.decls[decl.CELName()]; dup && prior != decl {
		u.errorf(p, where, "its expression name %s is already taken by %s", decl.CELName(), prior.Path())
		return
	}
	u.decls[decl.CELName()] = decl
}

func (u *Universe) resolveDecl(p *Package, d *Definition, decl *Decl, where string) {
	if decl.Kind == "record" || decl.Kind == "variant" {
		u.register(p, decl, where)
	}
	for _, fname := range sortedKeys(decl.Fields) {
		f := decl.Fields[fname]
		fwhere := where + "." + fname
		if f.TypeSrc == "" {
			continue // the shape check reports it
		}
		t, err := ParseType(f.TypeSrc)
		if err != nil {
			u.errorf(p, fwhere, "%v", err)
			continue
		}
		u.resolveType(p, d, t, fwhere)
		f.Type = t
		if f.Max > 0 && t.Kind != "list" && t.Kind != "map" && (t.Kind != "scalar" || t.Name != "string" && t.Name != "text") {
			u.errorf(p, fwhere, "max applies to strings, texts, lists and maps")
		}
		if !f.Enabled && f.Required {
			u.errorf(p, fwhere, "a field not enabled cannot be required")
		}
		if f.Key != "" {
			var rec *Decl
			if t.Kind == "list" && t.Elem.Kind == "named" && t.Elem.Decl != nil {
				rec = t.Elem.Decl
			}
			var key *Field
			if rec != nil && (rec.Kind == "record" || rec.Kind == "definition") {
				key = rec.Fields[f.Key]
			}
			if key == nil || !key.Enabled || key.Type == nil && key.TypeSrc == "" || !u.keyType(rec, key.TypeSrc) {
				u.errorf(p, fwhere, "key %s must be an enabled scalar or enumerated field of the records the list holds", f.Key)
			}
		}
	}
}

// keyType reports whether a field's type, written in the record rec, can identify a list item: a
// scalar or an enumeration, written inline or declared.
func (u *Universe) keyType(rec *Decl, src string) bool {
	t, err := ParseType(src)
	if err != nil {
		return false
	}
	switch t.Kind {
	case "scalar", "enum":
		return true
	case "named":
		p := u.Packages[rec.Package]
		if p == nil {
			return false
		}
		decl, err := u.lookup(p, p.Defs[rec.Definition], t.Name)
		return err == nil && decl.Kind == "enum"
	}
	return false
}

func (u *Universe) resolveType(p *Package, d *Definition, t *Type, where string) {
	switch t.Kind {
	case "list":
		u.resolveType(p, d, t.Elem, where)
	case "map":
		u.resolveType(p, d, t.Elem, where)
	case "named":
		decl, err := u.lookup(p, d, t.Name)
		if err != nil {
			u.errorf(p, where, "%v", err)
			return
		}
		t.Decl = decl
	case "ref":
		for _, r := range t.Refs {
			if r == "any" {
				continue
			}
			def, err := u.definitionNamed(p, r)
			if err != nil {
				u.errorf(p, where, "%v", err)
				continue
			}
			if !def.Decidable() {
				u.errorf(p, where, "ref(%s) can never be satisfied: %s has no recognize use and none of its operations names a use, so no fact of that kind is recognized", r, r)
			}
			t.Target = append(t.Target, def.Decl)
		}
	}
}

// markUsed records that package p names its dependency.
func (u *Universe) markUsed(p *Package, name string) {
	if u.used == nil {
		u.used = map[string]bool{}
	}
	u.used[p.Name+"|"+name] = true
}

func (u *Universe) dependency(p *Package, name string) (*Package, error) {
	if name == p.Name {
		return p, nil
	}
	if !contains(p.DependsOn, name) {
		return nil, fmt.Errorf("names package %s, which depends_on does not list", name)
	}
	if q := u.Packages[name]; q != nil {
		u.markUsed(p, name)
		return q, nil
	}
	return nil, fmt.Errorf("package %s is not loaded", name)
}

// Point resolves a decision point as package p names it: a point the format defines keeps its
// name, and a point a package declares is qualified by that package's name.
func (u *Universe) Point(p *Package, name string) (string, error) {
	if contains(OperationPoints, name) {
		return name, nil
	}
	q, point := p, name
	if pkgName, rest, ok := strings.Cut(name, "/"); ok {
		var err error
		if q, err = u.dependency(p, pkgName); err != nil {
			return "", err
		}
		point = rest
	}
	if !q.Points[point] {
		return "", fmt.Errorf("%s is neither a decision point the format defines nor one %s declares", name, q.Name)
	}
	return q.Name + "/" + point, nil
}

func (u *Universe) lookup(p *Package, d *Definition, name string) (*Decl, error) {
	if b := builtinRecords[name]; b != nil {
		return b, nil
	}
	if pkgName, rest, ok := strings.Cut(name, "/"); ok {
		q, err := u.dependency(p, pkgName)
		if err != nil {
			return nil, err
		}
		return lookupIn(q, nil, rest, name)
	}
	return lookupIn(p, d, name, name)
}

func lookupIn(p *Package, d *Definition, name, written string) (*Decl, error) {
	if defName, typeName, ok := strings.Cut(name, "."); ok {
		def := p.Defs[defName]
		if def == nil || def.Types[typeName] == nil {
			return nil, fmt.Errorf("type %s is not declared", written)
		}
		return def.Types[typeName], nil
	}
	var found []*Decl
	if d != nil && d.Types[name] != nil {
		found = append(found, d.Types[name])
	}
	if p.Types[name] != nil {
		found = append(found, p.Types[name])
	}
	if p.Defs[name] != nil {
		found = append(found, p.Defs[name].Decl)
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("type %s is not declared", written)
	case 1:
		return found[0], nil
	}
	return nil, fmt.Errorf("type %s resolves more than once", written)
}

func (u *Universe) definitionNamed(p *Package, name string) (*Definition, error) {
	q, defName := p, name
	if pkgName, rest, ok := strings.Cut(name, "/"); ok {
		var err error
		if q, err = u.dependency(p, pkgName); err != nil {
			return nil, err
		}
		defName = rest
	}
	if def := q.Defs[defName]; def != nil {
		return def, nil
	}
	return nil, fmt.Errorf("%s is not a definition", name)
}

// CELType is the type of a value of t in expressions.
func (u *Universe) CELType(t *Type) *types.Type {
	if t == nil {
		return types.DynType
	}
	switch t.Kind {
	case "scalar":
		return Scalars[t.Name]
	case "list":
		return types.NewListType(u.CELType(t.Elem))
	case "map":
		return types.NewMapType(types.StringType, u.CELType(t.Elem))
	case "enum":
		return types.StringType
	case "ref":
		if len(t.Target) == 1 && len(t.Refs) == 1 {
			return RefType(types.NewObjectType(t.Target[0].CELName()))
		}
		return RefType(types.DynType)
	case "named":
		if t.Decl == nil {
			return types.DynType
		}
		if t.Decl.Kind == "enum" {
			return types.StringType
		}
		return types.NewObjectType(t.Decl.CELName())
	}
	return types.DynType
}

// --- expressions ---

type provider struct {
	types.Provider
	adapter types.Adapter
	u       *Universe
}

func (pr *provider) FindStructType(name string) (*types.Type, bool) {
	if _, ok := pr.u.decls[name]; ok {
		return types.NewTypeTypeWithParam(types.NewObjectType(name)), true
	}
	return pr.Provider.FindStructType(name)
}

func (pr *provider) FindStructFieldNames(name string) ([]string, bool) {
	if d, ok := pr.u.decls[name]; ok {
		return sortedKeys(d.Fields), true
	}
	return pr.Provider.FindStructFieldNames(name)
}

func (pr *provider) FindStructFieldType(name, field string) (*types.FieldType, bool) {
	d, ok := pr.u.decls[name]
	if !ok {
		return pr.Provider.FindStructFieldType(name, field)
	}
	f, ok := d.Fields[field]
	if !ok {
		return nil, false
	}
	// An instance is held as a map, as it is parsed; an absent field reads as the format says.
	return &types.FieldType{
		Type:    pr.u.CELType(f.Type),
		IsSet:   func(obj any) bool { return fieldIsSet(obj, f) },
		GetFrom: func(obj any) (any, error) { return fieldValue(obj, f) },
	}, true
}

// NewValue builds a value constructed by naming its type. A value of a declared or built-in record
// is held as an instance is, a map of the fields it sets, so it reads its absent fields as an
// instance does and equals another that sets the same fields to equal values.
func (pr *provider) NewValue(name string, fields map[string]ref.Val) ref.Val {
	if d, ok := pr.u.decls[name]; ok && d.Kind != "enum" {
		m := make(map[string]any, len(fields))
		for k, v := range fields {
			if types.IsUnknownOrError(v) {
				return v
			}
			m[k] = v.Value()
		}
		return pr.adapter.NativeToValue(m)
	}
	return pr.Provider.NewValue(name, fields)
}

func (u *Universe) environments() error {
	reg, err := types.NewRegistry()
	if err != nil {
		return err
	}
	opts := []cel.EnvOption{
		standardLibrary(),
		cel.CustomTypeProvider(&provider{reg, reg, u}),
		cel.ParserExpressionSizeLimit(MaxExpressionCodePoints),
		cel.ExpressionNodeLimit(MaxExpressionNodes),
		cel.ParserRecursionLimit(MaxExpressionNesting),
	}
	opts = append(opts, sortByLibrary()...)
	opts = append(opts, possibleLibrary()...)
	var stateOpts []cel.EnvOption
	for _, f := range Library {
		switch {
		case f.Decl == nil:
		case f.State:
			stateOpts = append(stateOpts, f.Decl)
		default:
			opts = append(opts, f.Decl)
		}
	}
	opts = append(opts, u.combinationOfDecl(), u.combineDecl())
	stateOpts = append(stateOpts, u.findingsDecl("findings"), u.findingsDecl("candidate_findings"))
	if u.base, err = cel.NewCustomEnv(opts...); err != nil {
		return err
	}
	u.state, err = u.base.Extend(stateOpts...)
	return err
}

// findingsList is the type of the findings a check has: references to the kernel's finding
// definition, whose name in expressions follows the kernel's name. With no kernel named, the type
// has a name no package can give.
func (u *Universe) findingsList() *types.Type {
	name := "finding"
	if u.Kernel != "" {
		name = underscore(u.Kernel) + ".finding"
	}
	return types.NewListType(RefType(types.NewObjectType(name)))
}

// combineDecl declares combine, which takes the findings of a check and a combination.
func (u *Universe) combineDecl() cel.EnvOption {
	return cel.Function("combine", cel.Overload("combine_findings_combination", []*types.Type{u.findingsList(), combineType}, types.BoolType), cel.SingletonBinaryBinding(combine))
}

// findingsDecl declares findings and candidate_findings, which name a check and a position or
// revision and return the findings of that check.
func (u *Universe) findingsDecl(name string) cel.EnvOption {
	return cel.Function(name, cel.Overload(name+"_string_string", []*types.Type{types.StringType, types.StringType}, u.findingsList()))
}

// context is the environment one expression compiles in, and what it may read.
type context struct {
	p    *Package
	d    *Definition
	env  *cel.Env
	self *Decl // for cost estimation; nil outside formal checks
	// reads, when set, holds the only names an expression may read, as for a chain act's use
	// (chainActs); nil leaves what it reads to its environment alone.
	reads map[string]bool
}

func (u *Universe) checkContext(p *Package, d *Definition) (*context, error) {
	var opts []cel.EnvOption
	if d.HasStructure() {
		opts = append(opts, cel.Variable("self", types.NewObjectType(d.Decl.CELName())))
	}
	env, err := u.base.Extend(opts...)
	return &context{p, d, env, d.Decl, nil}, err
}

// useContext is the context of a use's conditions. Only a use that decides revisions reads the
// revision its fact supersedes.
func (u *Universe) useContext(p *Package, d *Definition, revises bool) (*context, error) {
	opts := []cel.EnvOption{cel.Variable("fact", factType)}
	if d.HasStructure() {
		self := types.NewObjectType(d.Decl.CELName())
		opts = append(opts, cel.Variable("self", self))
		if revises {
			opts = append(opts, cel.Variable("predecessor", RefType(self)))
		}
	}
	if kernel := u.Packages[u.Kernel]; kernel != nil && (p.Name == u.Kernel || contains(p.DependsOn, u.Kernel)) {
		if cp, tr := kernel.Defs[custodyDefinition], kernel.Defs[trustRootDefinition]; cp != nil && tr != nil {
			opts = append(opts,
				cel.Variable("custody", types.NewObjectType(cp.Decl.CELName())),
				cel.Variable("trust_root", types.NewObjectType(tr.Decl.CELName())))
		} else if u.contextMissing() {
			// The root cause is reported once (contextMissing). The variables stay declared, without a
			// type, so that no expression is reported for a name it uses correctly.
			opts = append(opts, cel.Variable("custody", types.DynType), cel.Variable("trust_root", types.DynType))
		}
	}
	env, err := u.state.Extend(opts...)
	return &context{p, d, env, nil, nil}, err
}

func (c *context) with(name string, t *types.Type) *context {
	env, err := c.env.Extend(cel.Variable(name, t))
	if err != nil {
		return c
	}
	return &context{c.p, c.d, env, c.self, c.reads}
}

func (u *Universe) reserved(name string) bool {
	switch name {
	case "self", "fact", "predecessor", "custody", "trust_root", "item", builtinPackage:
		return true
	}
	for pkg := range u.Packages {
		if underscore(pkg) == name {
			return true
		}
	}
	return false
}

// compile type-checks an expression; want, if given, is the type it must have.
func (u *Universe) compile(c *context, src any, where string, want *types.Type) *cel.Ast {
	s, ok := src.(string)
	if !ok || strings.TrimSpace(s) == "" {
		u.errorf(c.p, where, "must be an expression")
		return nil
	}
	a, iss := c.env.Compile(s)
	if iss != nil && iss.Err() != nil {
		for _, e := range iss.Errors() {
			u.errorf(c.p, where, "%d:%d: %s", e.Location.Line(), e.Location.Column(), e.Message)
		}
		return nil
	}
	if want != nil && !a.OutputType().IsExactType(want) && !emptyList(a, want) {
		u.errorf(c.p, where, "is %s, not %s", a.OutputType(), want)
		return nil
	}
	if !u.refuseOutsideTheLanguage(c.p, a, where) {
		return nil
	}
	u.lint(c.p, a, where)
	if c.reads != nil {
		u.chainActReads(c, a, where)
	}
	return a
}

// chainActs are the definitions of the kernel whose facts are the chain acts: the trust-root
// revisions and the compromise revocations. Verification judges them first, before it reads the
// anchors, any configuration or a grant, so a let or a condition of
// their uses reads only the instance, its fact and the trust root.
var chainActs = []string{trustRootDefinition, "compromise-revocation"}

// chainActVariables are the variables of a use's context that a chain act's expressions may read:
// self, fact and trust_root, and the item of an each, whose list they read the same way.
var chainActVariables = []string{"self", "fact", "trust_root", "item"}

// chainActContext returns the context of a use of def with what its expressions may read
// narrowed, when def is a chain act of the kernel: the variables above and the use's own lets,
// each of which is checked in the same way.
func (u *Universe) chainActContext(c *context, p *Package, d *Definition, um map[string]any) *context {
	if p.Name != u.Kernel || !contains(chainActs, d.Name) {
		return c
	}
	reads := map[string]bool{}
	for _, v := range chainActVariables {
		reads[v] = true
	}
	for name := range mapping(um["let"]) {
		reads[name] = true
	}
	return &context{c.p, c.d, c.env, c.self, reads}
}

// readsLog says whether a library function reads the log, a reference or the bytes the log
// carries: the functions that read the history, the functions that resolve a reference, and those
// that read retained bytes. A chain act reads none of them.
func readsLog(name string) bool {
	switch name {
	case "content", "fact", "retained", "quoted":
		return true
	}
	for _, f := range Library {
		if f.Name == name {
			return f.State
		}
	}
	return false
}

// chainActReads reports what an expression of a chain act's use reads beyond what c.reads allows,
// and each function it calls that reads the log.
func (u *Universe) chainActReads(c *context, a *cel.Ast, where string) {
	e := a.NativeRep().Expr()
	var bad []string
	seen := map[string]bool{}
	add := func(what string) {
		if !seen[what] {
			seen[what] = true
			bad = append(bad, what)
		}
	}
	for _, id := range freeIdentifiers(e, nil) {
		// A qualified name is a type, such as the kind history takes, and reads nothing itself.
		if !c.reads[id] && !strings.Contains(id, ".") {
			add(id)
		}
	}
	celast.PostOrderVisit(e, celast.NewExprVisitor(func(x celast.Expr) {
		if x.Kind() == celast.CallKind && readsLog(x.AsCall().FunctionName()) {
			add(x.AsCall().FunctionName() + "()")
		}
	}))
	if len(bad) > 0 {
		u.errorf(c.p, where, "reads %s, but a chain act is judged before the anchors, any configuration or a grant, so its lets and conditions read only self, fact and trust_root and call no function that reads the log", strings.Join(bad, ", "))
	}
}

// emptyList reports whether want is a list type and the expression is the literal [], which has
// every list type.
func emptyList(a *cel.Ast, want *types.Type) bool {
	e := a.NativeRep().Expr()
	return want.Kind() == types.ListKind && e.Kind() == celast.ListKind && len(e.AsList().Elements()) == 0
}

// letAst is one compiled let.
type letAst struct {
	name string
	ast  *cel.Ast
}

// lets adds named expressions to a context, in the order their references require.
func (u *Universe) lets(c *context, raw any, where string) (*context, []letAst) {
	m := mapping(raw)
	deps := map[string][]string{}
	for _, name := range sortedKeys(m) {
		if u.reserved(name) {
			u.errorf(c.p, where+".let."+name, "%s is a reserved name", name)
		}
		s, _ := m[name].(string)
		parsed, iss := c.env.Parse(s)
		if iss != nil && iss.Err() != nil {
			continue // compile reports it
		}
		for _, id := range freeIdentifiers(parsed.NativeRep().Expr(), nil) {
			if _, isLet := m[id]; isLet && id != name && !contains(deps[name], id) {
				deps[name] = append(deps[name], id)
			}
		}
	}
	var order []string
	state := map[string]int{} // 1 visiting, 2 done
	var visit func(string, []string)
	visit = func(n string, trail []string) {
		switch state[n] {
		case 1:
			u.errorf(c.p, where+".let", "names form a cycle: %s", strings.Join(append(trail, n), " -> "))
			return
		case 2:
			return
		}
		state[n] = 1
		for _, dep := range deps[n] {
			visit(dep, append(trail, n))
		}
		state[n] = 2
		order = append(order, n)
	}
	for _, name := range sortedKeys(m) {
		if state[name] == 0 {
			visit(name, nil)
		}
	}
	var compiled []letAst
	for _, name := range order {
		t := types.DynType
		if a := u.compile(c, m[name], where+".let."+name, nil); a != nil {
			t, compiled = a.OutputType(), append(compiled, letAst{name, a})
		}
		c = c.with(name, t)
	}
	return c, compiled
}

// freeIdentifiers returns the identifiers an expression reads that no comprehension within it binds.
func freeIdentifiers(e celast.Expr, bound map[string]bool) []string {
	var out []string
	var walk func(celast.Expr, map[string]bool)
	walk = func(e celast.Expr, bound map[string]bool) {
		switch e.Kind() {
		case celast.IdentKind:
			if !bound[e.AsIdent()] {
				out = append(out, e.AsIdent())
			}
		case celast.SelectKind:
			walk(e.AsSelect().Operand(), bound)
		case celast.CallKind:
			call := e.AsCall()
			if call.IsMemberFunction() {
				walk(call.Target(), bound)
			}
			for _, a := range call.Args() {
				walk(a, bound)
			}
		case celast.ListKind:
			for _, el := range e.AsList().Elements() {
				walk(el, bound)
			}
		case celast.MapKind:
			for _, entry := range e.AsMap().Entries() {
				walk(entry.AsMapEntry().Key(), bound)
				walk(entry.AsMapEntry().Value(), bound)
			}
		case celast.StructKind:
			for _, f := range e.AsStruct().Fields() {
				walk(f.AsStructField().Value(), bound)
			}
		case celast.ComprehensionKind:
			comp := e.AsComprehension()
			walk(comp.IterRange(), bound)
			walk(comp.AccuInit(), bound)
			inner := map[string]bool{comp.IterVar(): true, comp.AccuVar(): true}
			if comp.HasIterVar2() {
				inner[comp.IterVar2()] = true
			}
			for k := range bound {
				inner[k] = true
			}
			walk(comp.LoopCondition(), inner)
			walk(comp.LoopStep(), inner)
			walk(comp.Result(), inner)
		}
	}
	walk(e, bound)
	return out
}

// --- definitions ---

func (u *Universe) definition(p *Package, d *Definition) {
	checks := mapping(d.Raw["checks"])
	formal := mapping(checks["formal"])
	for _, cname := range sortedKeys(formal) {
		where := d.Name + ".checks.formal." + cname
		cm := mapping(formal[cname])
		c, err := u.checkContext(p, d)
		if err != nil {
			u.errorf(p, where, "%v", err)
			continue
		}
		c, lets := u.lets(c, cm["let"], where)
		if a := u.compile(c, cm["expr"], where+".expr", types.BoolType); a != nil {
			u.cost(c, lets, a, where)
		}
	}
	uses := mapping(d.Raw["uses"])
	for _, uname := range sortedKeys(uses) {
		where := d.Name + ".uses." + uname
		um := mapping(uses[uname])
		c, err := u.useContext(p, d, um["revises"] == true)
		if err != nil {
			u.errorf(p, where, "%v", err)
			continue
		}
		c = u.chainActContext(c, p, d, um)
		c, _ = u.lets(c, um["let"], where)
		for _, k := range []string{"candidate", "beneficiaries", "accepts_declared", "independent"} {
			if v, ok := um[k]; ok {
				want := stringList
				if k == "candidate" {
					want = types.StringType
				}
				if a := u.compile(c, v, where+"."+k, want); a != nil && k == "independent" {
					u.independentKinds(p, a, where+"."+k)
					if b, ok := um["beneficiaries"].(string); ok && strings.TrimSpace(b) == "[]" {
						u.errorf(p, where+".independent", "the use benefits no party, so no decision can be outside every beneficiary's reach")
					}
				}
			}
		}
		for _, s := range list(um["requires_satisfied"]) {
			name, _ := s.(string)
			path := d.Name + ".checks.formal." + name
			if mapping(formal[name]) == nil {
				path = d.Name + ".checks.semantic." + name
			}
			u.condition(c, map[string]any{"check": path}, where+".requires_satisfied."+name)
		}
		requires := mapping(um["requires"])
		for _, cname := range sortedKeys(requires) {
			u.condition(c, mapping(requires[cname]), where+".requires."+cname)
		}
	}
	ops := mapping(d.Raw["operations"])
	for _, oname := range sortedKeys(ops) {
		om := mapping(ops[oname])
		where := d.Name + ".operations." + oname
		if at := fmt.Sprint(om["at"]); strings.Contains(at, "/") {
			if _, err := u.Point(p, at); err != nil {
				u.errorf(p, where+".at", "%v", err)
			}
		}
		for _, k := range []string{"use", "decides"} {
			if v, ok := om[k]; ok && mapping(mapping(d.Raw["uses"])[fmt.Sprint(v)]) == nil {
				u.errorf(p, where+"."+k, "%v is not a use of %s", v, d.Name)
			}
		}
		for _, e := range list(om["performs"]) {
			if _, parts, ok := u.element(p, e, where+".performs"); ok && Shape(parts) != "operation" {
				u.errorf(p, where+".performs", "%v is not an operation", e)
			}
		}
		u.bindsCheck(p, d, oname, om)
	}
}

func (u *Universe) condition(c *context, cm map[string]any, where string) {
	var item *types.Type
	if each, ok := cm["each"]; ok {
		if a := u.compile(c, each, where+".each", nil); a != nil {
			if t := a.OutputType(); t.Kind() != types.ListKind {
				u.errorf(c.p, where+".each", "is %s, not a list", t)
			} else {
				item = t.Parameters()[0]
				c = c.with("item", item)
			}
		}
		if addr, ok := cm["address"]; ok {
			u.compile(c, addr, where+".address", types.StringType)
		}
	}
	// value is the type a check or use condition applies to; ok is false when it could not be
	// determined because an expression failed to compile, which compile has reported.
	value := func() (*types.Type, bool) {
		if of, ok := cm["of"]; ok {
			if a := u.compile(c, of, where+".of", nil); a != nil {
				return a.OutputType(), true
			}
			return nil, false
		}
		if _, ok := cm["each"]; ok {
			return item, item != nil
		}
		if c.d.HasStructure() {
			return types.NewObjectType(c.d.Decl.CELName()), true
		}
		return nil, true
	}
	switch {
	case cm["expr"] != nil:
		u.compile(c, cm["expr"], where+".expr", types.BoolType)
	case cm["check"] != nil:
		def, parts, ok := u.element(c.p, cm["check"], where+".check")
		if !ok {
			return
		}
		if Shape(parts) != "check" {
			u.errorf(c.p, where+".check", "%v is not a check", cm["check"])
			return
		}
		if parts[2] == "semantic" && mapping(mapping(mapping(def.Raw["checks"])["semantic"])[parts[3]])["combination"] == nil {
			u.errorf(c.p, where+".check", "%v is a semantic check that declares no combination, so it cannot decide a condition", cm["check"])
		}
		if parts[2] == "semantic" && (cm["of"] != nil || cm["each"] != nil) {
			u.errorf(c.p, where+".check", "%v is semantic, so it is decided for a revision of the instance, never for a value inside it: it takes neither of nor each", cm["check"])
			return
		}
		if t, ok := value(); ok {
			u.applies(c, def, t, where)
		}
	case cm["use"] != nil:
		def, parts, ok := u.element(c.p, cm["use"], where+".use")
		if !ok {
			return
		}
		if Shape(parts) != "use" {
			u.errorf(c.p, where+".use", "%v is not a use", cm["use"])
			return
		}
		target := mapping(mapping(def.Raw["uses"])[parts[2]])
		requires := mapping(target["requires"])
		if cm["of"] != nil || cm["each"] != nil {
			semantic := false
			for _, s := range list(target["requires_satisfied"]) {
				name, _ := s.(string)
				semantic = semantic || mapping(mapping(def.Raw["checks"])["semantic"])[name] != nil
			}
			for _, name := range sortedKeys(requires) {
				semantic = semantic || strings.Contains(fmt.Sprint(mapping(requires[name])["check"]), ".checks.semantic.")
			}
			if semantic {
				u.errorf(c.p, where+".use", "%v decides a semantic check, so it is decided for a revision of the instance, never for a value inside it: it takes neither of nor each", cm["use"])
				return
			}
		}
		for _, name := range sortedKeys(requires) {
			cond := mapping(requires[name])
			if cond["check"] == nil || cond["of"] != nil || cond["each"] != nil {
				u.errorf(c.p, where+".use", "%v decides %s other than by a check of its own instance, so it cannot be applied to a value", cm["use"], name)
			}
		}
		if t, ok := value(); ok {
			u.applies(c, def, t, where)
		}
	case cm["interpreter"] != nil:
		if !contains(Capabilities, fmt.Sprint(cm["interpreter"])) {
			u.errorf(c.p, where+".interpreter", "%v is not an interpreter capability", cm["interpreter"])
		}
	}
}

// applies reports whether a check or use of def can be applied to a value of type t.
func (u *Universe) applies(c *context, def *Definition, t *types.Type, where string) {
	want := types.NewObjectType(def.Decl.CELName())
	if t == nil {
		u.errorf(c.p, where, "applies to no value: give of or each")
	} else if !t.IsExactType(want) {
		u.errorf(c.p, where, "applies to %s, but %s is a definition of %s", t, def.Pkg.Name+"/"+def.Name, want)
	}
}

// Shape names what an element path designates: structure, field, meaning, check, operation,
// use or condition; or "" if it designates nothing a rule or condition may name.
func Shape(parts []string) string {
	if len(parts) < 2 {
		return ""
	}
	switch kind, n := parts[1], len(parts); {
	case kind == "structure" && n == 2:
		return "structure"
	case kind == "structure" && n == 3:
		return "field"
	case kind == "meaning" && (n == 2 || n == 3):
		return "meaning"
	case kind == "checks" && n == 4 && (parts[2] == "formal" || parts[2] == "semantic"):
		return "check"
	case kind == "operations" && n == 3:
		return "operation"
	case kind == "uses" && n == 3:
		return "use"
	case kind == "uses" && n == 5 && parts[3] == "requires":
		return "condition"
	}
	return ""
}

// Element resolves an element path from package p. A path into another package must name a
// dependency of p, unless anyPackage is set, as for a rule, which any package of a release may implement.
func (u *Universe) Element(p *Package, path string, anyPackage bool) (*Definition, []string, any, error) {
	q := p
	if pkgName, rest, ok := strings.Cut(path, "/"); ok {
		var err error
		if q, err = u.dependency(p, pkgName); err != nil {
			if q = u.Packages[pkgName]; q == nil || !anyPackage {
				return nil, nil, nil, err
			}
		}
		path = rest
	}
	parts := strings.Split(path, ".")
	def := q.Defs[parts[0]]
	if def == nil {
		return nil, nil, nil, fmt.Errorf("%s is not a definition", parts[0])
	}
	if node, ok := shorthandCondition(def, parts); ok {
		return def, parts, node, nil
	}
	var node any = def.Raw
	for _, part := range parts[1:] {
		m, ok := node.(map[string]any)
		if !ok || m[part] == nil {
			return nil, nil, nil, fmt.Errorf("%s does not exist", path)
		}
		node = m[part]
	}
	return def, parts, node, nil
}

// shorthandCondition resolves the element path of a condition that requires_satisfied stands for:
// the check condition named after a check of the definition, which a use lists in requires_satisfied
// and does not write under requires.
func shorthandCondition(def *Definition, parts []string) (map[string]any, bool) {
	if len(parts) != 5 || parts[1] != "uses" || parts[3] != "requires" {
		return nil, false
	}
	use := mapping(mapping(def.Raw["uses"])[parts[2]])
	for _, s := range list(use["requires_satisfied"]) {
		if name, _ := s.(string); name == parts[4] {
			kind := "formal"
			if mapping(mapping(def.Raw["checks"])["formal"])[name] == nil {
				kind = "semantic"
			}
			return map[string]any{"check": def.Name + ".checks." + kind + "." + name}, true
		}
	}
	return nil, false
}

func (u *Universe) element(p *Package, v any, where string) (*Definition, []string, bool) {
	s, _ := v.(string)
	def, parts, _, err := u.Element(p, s, false)
	if err != nil {
		u.errorf(p, where, "%v", err)
		return nil, nil, false
	}
	return def, parts, true
}

// --- lint ---

// lint checks what types cannot: string literals compared with enumerated fields, and the
// element paths that authorized, findings and holds name.
func (u *Universe) lint(p *Package, a *cel.Ast, where string) {
	native := a.NativeRep()
	named := map[string]bool{}
	// claimed holds the string literals a check of their own has read, so the check that every
	// literal naming an element resolves does not report them twice.
	claimed := map[int64]bool{}
	var literals []celast.Expr
	celast.PostOrderVisit(native.Expr(), celast.NewExprVisitor(func(e celast.Expr) {
		if t := native.GetType(e.ID()); t != nil {
			u.packagesNamed(t, named)
		}
		if _, ok := stringLiteral(e); ok {
			literals = append(literals, e)
		}
		if e.Kind() != celast.CallKind {
			return
		}
		call := e.AsCall()
		args := call.Args()
		switch call.FunctionName() {
		case "_==_", "_!=_":
			u.enumLiteral(p, native, args[0], args[1], false, where)
			u.enumLiteral(p, native, args[1], args[0], false, where)
			u.kindLiteral(p, native, args[0], args[1], claimed, where)
			u.kindLiteral(p, native, args[1], args[0], claimed, where)
		case "_[_]":
			if isDecisionPoints(args[0]) {
				u.pointLiteral(p, args[1], claimed, where)
			}
		case "@in":
			if isDecisionPoints(args[1]) {
				u.pointLiteral(p, args[0], claimed, where)
			}
			if args[1].Kind() == celast.ListKind {
				for _, el := range args[1].AsList().Elements() {
					u.enumLiteral(p, native, args[0], el, false, where)
					u.kindLiteral(p, native, args[0], el, claimed, where)
				}
			} else {
				u.enumLiteral(p, native, args[1], args[0], true, where)
			}
		case "authorized":
			if s, ok := stringLiteral(args[1]); ok {
				u.action(p, s, where)
			}
		case "holds", "permitted":
			if s, ok := stringLiteral(args[1]); ok {
				u.heldUse(p, native, args[0], call.FunctionName(), s, where)
			}
		case "combination_of":
			u.combinationOfArgument(p, args[0], where)
		case "findings", "candidate_findings":
			if s, ok := stringLiteral(args[0]); ok {
				if _, parts, _, err := u.Element(p, s, false); err != nil || Shape(parts) != "check" || !strings.Contains(s, "/") {
					u.errorf(p, where, "%s names %q, which is not a check's element path qualified by package", call.FunctionName(), s)
				}
			}
		}
	}))
	for _, lit := range literals {
		if !claimed[lit.ID()] {
			u.elementLiteral(p, lit, where)
		}
	}
	for _, name := range sortedKeys(named) {
		if name != p.Name && !contains(p.DependsOn, name) {
			u.errorf(p, where, "names package %s, which depends_on does not list", name)
		} else {
			u.markUsed(p, name)
		}
	}
}

// packagesNamed collects the packages whose types a type, or a type it holds, names.
func (u *Universe) packagesNamed(t *types.Type, into map[string]bool) {
	if t.Kind() == types.StructKind {
		if decl := u.decls[t.TypeName()]; decl != nil && !decl.Builtin {
			into[decl.Package] = true
		}
	}
	for _, param := range t.Parameters() {
		u.packagesNamed(param, into)
	}
}

// combinationOfArgument checks the argument of combination_of: a string literal that is the
// element path, qualified by package, of a semantic check that declares a combination. The value is
// then fixed when the packages are read.
func (u *Universe) combinationOfArgument(p *Package, arg celast.Expr, where string) {
	s, ok := stringLiteral(arg)
	if !ok {
		u.errorf(p, where, "combination_of takes a string literal, the element path of a semantic check, so that its value is fixed when the package is read")
		return
	}
	def, parts, _, err := u.Element(p, s, false)
	switch {
	case err != nil:
		u.errorf(p, where, "combination_of names %q: %v", s, err)
	case !strings.Contains(s, "/"):
		u.errorf(p, where, "combination_of names %q, which is not an element path qualified by package", s)
	case Shape(parts) != "check" || parts[2] != "semantic":
		u.errorf(p, where, "combination_of names %q, which is not a semantic check", s)
	case mapping(mapping(mapping(def.Raw["checks"])["semantic"])[parts[3]])["combination"] == nil:
		u.errorf(p, where, "combination_of names %q, a semantic check that declares no combination", s)
	}
}

// combinationOfDecl declares combination_of, whose value is the combination the semantic check it
// names declares. The checker has refused any argument but a literal that names such a check, so
// the value reads no log.
func (u *Universe) combinationOfDecl() cel.EnvOption {
	return cel.Function("combination_of",
		cel.Overload("combination_of_string", []*types.Type{types.StringType}, combineType, cel.UnaryBinding(func(arg ref.Val) ref.Val {
			path, ok := arg.Value().(string)
			if !ok {
				return types.NewErr("combination_of: the argument is not a string")
			}
			pkgName, rest, _ := strings.Cut(path, "/")
			parts := strings.Split(rest, ".")
			if p := u.Packages[pkgName]; p != nil && len(parts) == 4 && p.Defs[parts[0]] != nil {
				raw := mapping(mapping(mapping(mapping(p.Defs[parts[0]].Raw["checks"])["semantic"])[parts[3]])["combination"])
				if raw != nil {
					return types.DefaultTypeAdapter.NativeToValue(map[string]any{"rule": raw["rule"], "cap": raw["cap"]})
				}
			}
			return types.NewErr("combination_of: %q names no semantic check that declares a combination", path)
		})))
}

func stringLiteral(e celast.Expr) (string, bool) {
	if e.Kind() != celast.LiteralKind {
		return "", false
	}
	s, ok := e.AsLiteral().Value().(string)
	return s, ok
}

func (u *Universe) enumLiteral(p *Package, native *celast.AST, field, lit celast.Expr, elem bool, where string) {
	s, ok := stringLiteral(lit)
	if !ok || field.Kind() != celast.SelectKind {
		return
	}
	sel := field.AsSelect()
	opType := native.GetType(sel.Operand().ID())
	if opType == nil || opType.Kind() != types.StructKind {
		return
	}
	decl := u.decls[opType.TypeName()]
	if decl == nil || decl.Fields[sel.FieldName()] == nil {
		return
	}
	t := decl.Fields[sel.FieldName()].Type
	if elem {
		if t == nil || t.Kind != "list" {
			return
		}
		t = t.Elem
	}
	if t == nil {
		return
	}
	if values := t.EnumValues(); values != nil && !contains(values, s) {
		u.errorf(p, where, "%q is not a value of %s (%s)", s, sel.FieldName(), strings.Join(values, ", "))
	}
}

// kindLiteral checks a string literal compared with the kind of a fact: it names a definition as
// package/definition, of the package itself or of a package its depends_on lists, so a kind named
// only in a string still counts as naming its package.
func (u *Universe) kindLiteral(p *Package, native *celast.AST, field, lit celast.Expr, claimed map[int64]bool, where string) {
	s, ok := stringLiteral(lit)
	if !ok || field.Kind() != celast.SelectKind || field.AsSelect().FieldName() != "kind" {
		return
	}
	if t := native.GetType(field.AsSelect().Operand().ID()); t == nil || !t.IsExactType(factType) {
		return
	}
	claimed[lit.ID()] = true
	if !strings.Contains(s, "/") {
		u.errorf(p, where, "%q is compared with a fact's kind but is not a kind as package/definition", s)
		return
	}
	if _, err := u.definitionNamed(p, s); err != nil {
		u.errorf(p, where, "%v", err)
	}
}

// elementName is the shape of a string that names a definition or a decision point of a package:
// package/name, with no further path.
var elementName = regexp.MustCompile(`^([a-z][a-z0-9-]*)/([a-z][a-z0-9-]*)$`)

// elementLiteral checks a string literal wherever it stands in an expression. One of the shape
// package/name whose package is loaded names a definition or a decision point of it, so a renamed
// or removed definition cannot leave a credential list, a list of declared kinds or any other
// comparison naming nothing. The package must be the package itself or one that depends_on lists.
func (u *Universe) elementLiteral(p *Package, lit celast.Expr, where string) {
	s, _ := stringLiteral(lit)
	if msg := u.elementProblem(p, s); msg != "" {
		u.errorf(p, where, "%s", msg)
	}
}

// elementProblem says what is wrong with a string of the shape package/name whose package is
// loaded, or returns "" when it names a definition or a decision point of an allowed package.
func (u *Universe) elementProblem(p *Package, s string) string {
	m := elementName.FindStringSubmatch(s)
	if m == nil || u.Packages[m[1]] == nil {
		return ""
	}
	q := p
	if m[1] != p.Name {
		var err error
		if q, err = u.dependency(p, m[1]); err != nil {
			return err.Error()
		}
	}
	if q.Defs[m[2]] == nil && !q.Points[m[2]] {
		return fmt.Sprintf("%s is not a definition or a decision point of package %s", s, q.Name)
	}
	return ""
}

// isDecisionPoints says whether an expression is the custody profile's map of decision points.
func isDecisionPoints(e celast.Expr) bool {
	return e.Kind() == celast.SelectKind && e.AsSelect().FieldName() == "decision_points"
}

// pointLiteral checks a string literal that indexes the custody profile's decision points or tests
// membership in them: it names a decision point the format defines, or one a package declares,
// written package/name. A name that an expression computes cannot be checked here.
func (u *Universe) pointLiteral(p *Package, key celast.Expr, claimed map[int64]bool, where string) {
	s, ok := stringLiteral(key)
	if !ok {
		return
	}
	claimed[key.ID()] = true
	if _, err := u.Point(p, s); err != nil {
		u.errorf(p, where, "%v", err)
	}
}

// heldUse checks that holds, or permitted, names a use of the referenced definition, when the
// reference's type says which definition that is.
func (u *Universe) heldUse(p *Package, native *celast.AST, ref celast.Expr, function, use, where string) {
	t := native.GetType(ref.ID())
	if t == nil || len(t.Parameters()) != 1 {
		return
	}
	if t.Parameters()[0].Kind() != types.StructKind {
		// A reference to any definition: the use must at least exist in some package, or the
		// condition stays unknown for ever.
		if !u.declaresUse(use) {
			u.errorf(p, where, "%s names %q, which no package declares as a use", function, use)
		}
		if held := (HeldUse{function, use, where}); !containsHeld(p.AnyHolds, held) {
			p.AnyHolds = append(p.AnyHolds, held)
		}
		return
	}
	decl := u.decls[t.Parameters()[0].TypeName()]
	if decl == nil || decl.Kind != "definition" {
		return
	}
	def := u.Packages[decl.Package].Defs[decl.Definition]
	if mapping(mapping(def.Raw["uses"])[use]) == nil {
		u.errorf(p, where, "%s names %q, which is not a use of %s/%s", function, use, decl.Package, decl.Definition)
	}
}

func containsHeld(l []HeldUse, h HeldUse) bool {
	for _, e := range l {
		if e == h {
			return true
		}
	}
	return false
}

// declaresUse says whether some definition of some package declares a use of that name.
func (u *Universe) declaresUse(use string) bool {
	for _, p := range u.Packages {
		if p.DeclaresUse(use) {
			return true
		}
	}
	return false
}

// independentKinds checks the kinds that an independent key lists as literals: each must be a
// definition whose instances name the use and the candidate they decide for, in string fields
// use and candidate, as an exception does. A kind computed by an expression cannot be checked here.
func (u *Universe) independentKinds(p *Package, a *cel.Ast, where string) {
	for _, list := range literalLists(a.NativeRep().Expr()) {
		u.independentList(p, list, where)
	}
}

// literalLists returns the list literals an expression can yield: itself when it is one, and those
// of both branches of a conditional.
func literalLists(e celast.Expr) []celast.Expr {
	switch {
	case e.Kind() == celast.ListKind:
		return []celast.Expr{e}
	case e.Kind() == celast.CallKind && e.AsCall().FunctionName() == "_?_:_":
		args := e.AsCall().Args()
		return append(literalLists(args[1]), literalLists(args[2])...)
	}
	return nil
}

func (u *Universe) independentList(p *Package, native celast.Expr, where string) {
	for _, el := range native.AsList().Elements() {
		kind, ok := stringLiteral(el)
		if !ok {
			continue
		}
		def, err := u.definitionNamed(p, kind)
		if err != nil {
			// The check of every string literal has reported a name that is neither a definition
			// nor a decision point; this one reports a decision point, which decides nothing.
			if u.elementProblem(p, kind) == "" {
				u.errorf(p, where, "%v", err)
			}
			continue
		}
		for _, field := range []string{"use", "candidate"} {
			f := def.Decl.Fields[field]
			if f == nil || f.Type == nil || f.Type.Kind != "scalar" || f.Type.Name != "string" {
				u.errorf(p, where, "%s has no string field %s, so it cannot name the use and candidate it decides for", kind, field)
			}
		}
	}
}

func (u *Universe) action(p *Package, s, where string) {
	_, parts, _, err := u.Element(p, strings.TrimSuffix(s, ".except"), false)
	if err != nil || !strings.Contains(s, "/") || Shape(parts) != "use" {
		u.errorf(p, where, "authorized names %q, which is not a use (or a use followed by .except) qualified by package", s)
	}
}

// --- cost ---

const unbounded = ^uint64(0)

func addSat(a, b uint64) uint64 {
	if a > unbounded-b {
		return unbounded
	}
	return a + b
}

func mulSat(a, b uint64) uint64 {
	if a != 0 && b > unbounded/a {
		return unbounded
	}
	return a * b
}

type estimator struct {
	u          *Universe
	self       *Decl
	types      map[int64]*types.Type
	lets       map[string]uint64 // the largest size of each let that holds a list, map or string
	unresolved *bool             // set when the estimate reads a type that did not resolve
}

// resolved reports whether t resolved, marking the estimate unresolved if it did not: what a
// type that did not resolve costs is unknown, and its error is reported where it is written.
func (e estimator) resolved(t *Type) bool {
	if t.Resolved() {
		return true
	}
	*e.unresolved = true
	return false
}

func (e estimator) EstimateSize(n checker.AstNode) *checker.SizeEstimate {
	if x := n.Expr(); x != nil && x.Kind() == celast.IdentKind {
		if isBoundInput(x.AsIdent()) {
			return nil // the size of what a macro bound once is the size of the expression it bound
		}
		if size, ok := e.lets[x.AsIdent()]; ok {
			return &checker.SizeEstimate{Min: 0, Max: size}
		}
	}
	if max, ok := e.selectedSize(n.Expr()); ok {
		return &checker.SizeEstimate{Min: 0, Max: max}
	}
	if max, ok := e.declaredSize(n.Path()); ok {
		return &checker.SizeEstimate{Min: 0, Max: max}
	}
	switch n.Type().Kind() {
	case types.ListKind, types.MapKind:
		return &checker.SizeEstimate{Min: 0, Max: DefaultListMax}
	case types.StringKind:
		return &checker.SizeEstimate{Min: 0, Max: DefaultTextMax}
	}
	return nil
}

// size is the largest size a node may have.
func (e estimator) size(n checker.AstNode) uint64 {
	if s := n.ComputedSize(); s != nil {
		return s.Max
	}
	if s := e.EstimateSize(n); s != nil {
		return s.Max
	}
	return unbounded
}

func (e estimator) declaredSize(path []string) (uint64, bool) {
	if len(path) < 2 || path[0] != "self" || e.self == nil {
		return 0, false
	}
	t := &Type{Kind: "named", Decl: e.self}
	var spec *Field
	for _, step := range path[1:] {
		switch step {
		case "@items", "@values":
			if t.Kind != "list" && t.Kind != "map" {
				return 0, false
			}
			t, spec = t.Elem, nil
		case "@keys":
			if t.Kind != "map" {
				return 0, false
			}
			t, spec = t.Key, nil
		default:
			if t.Kind != "named" || t.Decl == nil || t.Decl.Fields[step] == nil {
				return 0, false
			}
			spec = t.Decl.Fields[step]
			t = spec.Type
		}
		if !e.resolved(t) {
			return 0, false
		}
	}
	return sizeOf(t, spec)
}

// selectedSize is the declared size of a field selected from a record, whatever expression
// produced the record.
func (e estimator) selectedSize(x celast.Expr) (uint64, bool) {
	if x == nil || x.Kind() != celast.SelectKind {
		return 0, false
	}
	sel := x.AsSelect()
	op := e.types[sel.Operand().ID()]
	if op == nil || op.Kind() != types.StructKind {
		return 0, false
	}
	decl := e.u.decls[op.TypeName()]
	if decl == nil || decl.Fields[sel.FieldName()] == nil {
		return 0, false
	}
	f := decl.Fields[sel.FieldName()]
	if !e.resolved(f.Type) {
		return 0, false
	}
	return sizeOf(f.Type, f)
}

// sizeOf is the largest size a value of type t may have under spec.
func sizeOf(t *Type, spec *Field) (uint64, bool) {
	if spec != nil && spec.Max > 0 {
		return uint64(spec.Max), true
	}
	switch {
	case t.Kind == "list" || t.Kind == "map":
		return DefaultListMax, true
	case t.Kind == "scalar" && t.Name == "text":
		return DefaultTextMax, true
	case t.Kind == "scalar" && t.Name == "digest":
		return digestSize, true
	case t.Kind == "scalar" || t.Kind == "enum" || t.Kind == "named" && t.Decl != nil && t.Decl.Kind == "enum":
		return DefaultStringMax, true
	}
	return 0, false
}

// digestSize is the length of sha256: and 64 hexadecimal digits.
const digestSize = 71

// EstimateCallCost costs each function of the library as interpretation revision 1 fixes its cost.
func (e estimator) EstimateCallCost(function, overloadID string, target *checker.AstNode, args []checker.AstNode) *checker.CallEstimate {
	tenth := func(size uint64) *checker.CallEstimate {
		return &checker.CallEstimate{CostEstimate: checker.CostEstimate{Min: 1, Max: addSat(size/10, 1)}}
	}
	switch function {
	case "quoted":
		if len(args) == 3 {
			return tenth(addSat(QuotedTextMax, e.size(args[2])))
		}
	case "retained":
		if len(args) == 1 {
			return tenth(e.size(args[0]))
		}
	case "matches_name":
		if len(args) == 2 {
			return tenth(mulSat(e.size(args[0]), e.size(args[1])))
		}
	case "combine":
		if len(args) == 2 {
			return &checker.CallEstimate{CostEstimate: checker.CostEstimate{Min: 1, Max: addSat(e.size(args[0]), 1)}}
		}
	case existsFn, allFn, filterFn, mapFn:
		// Decided from the statuses of the items, one pass over them; the lists they build are no
		// larger than the list they read.
		if len(args) == 2 {
			n := e.size(args[0])
			est := &checker.CallEstimate{CostEstimate: checker.CostEstimate{Min: 1, Max: addSat(n, 1)}}
			if function == filterFn || function == mapFn {
				est.ResultSize = &checker.SizeEstimate{Min: 0, Max: n}
			}
			return est
		}
	case "digest":
		return &checker.CallEstimate{CostEstimate: checker.CostEstimate{Min: 1, Max: 1}, ResultSize: &checker.SizeEstimate{Min: digestSize, Max: digestSize}}
	case "content", "fact":
		return &checker.CallEstimate{CostEstimate: checker.CostEstimate{Min: 1, Max: 1}}
	case "distinct", "flatten", "slice", "take", sortByKeys:
		if target != nil {
			return e.listCall(function, *target)
		}
	case "_==_", "_!=_":
		// Comparing records costs what comparing every field at its declared size costs; a
		// record that can contain itself has no such bound.
		if len(args) == 2 && args[0].Type().Kind() == types.StructKind {
			if decl := e.u.decls[args[0].Type().TypeName()]; decl != nil {
				cost, ok := e.equality(&Type{Kind: "named", Decl: decl}, nil, map[*Decl]bool{})
				if !ok {
					cost = unbounded
				}
				return &checker.CallEstimate{CostEstimate: checker.CostEstimate{Min: 1, Max: cost}}
			}
		}
	}
	return nil
}

// listCall estimates the list functions the format defines, on a list of at most n items: what
// each costs, and how large a list it returns.
func (e estimator) listCall(function string, target checker.AstNode) *checker.CallEstimate {
	n := e.size(target)
	result := func(cost, size uint64) *checker.CallEstimate {
		return &checker.CallEstimate{CostEstimate: checker.CostEstimate{Min: 1, Max: addSat(cost, 1)}, ResultSize: &checker.SizeEstimate{Min: 0, Max: size}}
	}
	switch function {
	case "distinct":
		return result(mulSat(n, n), n) // each item is compared with those kept before it
	case "flatten":
		inner := e.size(itemsOf{target})
		return result(mulSat(n, inner), mulSat(n, inner))
	case "slice", "take":
		return result(n, n)
	}
	steps := uint64(1) // sortBy: a sort, then a pass that finds equal keys
	for k := n; k > 1; k >>= 1 {
		steps++
	}
	return result(mulSat(n, steps), n)
}

// itemsOf is the node of a list's items, whose size is the size each item may have.
type itemsOf struct{ list checker.AstNode }

func (i itemsOf) Path() []string {
	if p := i.list.Path(); len(p) > 0 {
		return append(append([]string(nil), p...), "@items")
	}
	return nil
}
func (i itemsOf) Type() *types.Type {
	if t := i.list.Type(); t.Kind() == types.ListKind && len(t.Parameters()) == 1 {
		return t.Parameters()[0]
	}
	return types.DynType
}
func (i itemsOf) Expr() celast.Expr                   { return nil }
func (i itemsOf) ComputedSize() *checker.SizeEstimate { return nil }

// equality is what comparing two values of type t costs at their declared sizes; ok is false if
// nothing bounds it, as for a record that can contain itself. A type that did not resolve, which
// is reported where it is declared, costs nothing and marks the estimate unresolved.
func (e estimator) equality(t *Type, spec *Field, visiting map[*Decl]bool) (cost uint64, ok bool) {
	size := func(def uint64) uint64 {
		if spec != nil && spec.Max > 0 {
			return uint64(spec.Max)
		}
		return def
	}
	if !e.resolved(t) {
		return 0, true
	}
	switch t.Kind {
	case "scalar":
		switch t.Name {
		case "text":
			return size(DefaultTextMax)/10 + 1, true
		case "integer", "boolean", "position", "time":
			return 1, true
		case "digest":
			return digestSize/10 + 1, true
		}
		return size(DefaultStringMax)/10 + 1, true
	case "enum", "ref":
		return 8, true
	case "list", "map":
		elem, ok := e.equality(t.Elem, nil, visiting)
		return mulSat(size(DefaultListMax), elem), ok
	case "named":
		if visiting[t.Decl] {
			return 0, false
		}
		if t.Decl.Kind == "enum" {
			return 8, true
		}
		visiting[t.Decl] = true
		defer delete(visiting, t.Decl)
		var total uint64 = 1
		for _, f := range t.Decl.Fields {
			c, ok := e.equality(f.Type, f, visiting)
			if !ok {
				return 0, false
			}
			total = addSat(total, c)
		}
		return total, true
	}
	return 0, false
}

// cost refuses a formal check whose worst case, at every declared size, could exceed the limit.
// A let that holds a list, map or string is as large as the field it selects, and otherwise
// unbounded.
func (u *Universe) cost(c *context, lets []letAst, expr *cel.Ast, where string) {
	sizes := map[string]uint64{}
	unresolved := false
	using := func(a *cel.Ast) estimator {
		return estimator{u: u, self: c.self, types: a.NativeRep().TypeMap(), lets: sizes, unresolved: &unresolved}
	}
	var total uint64
	estimate := func(a *cel.Ast) bool {
		est, err := c.env.EstimateCost(a, using(a))
		if err != nil {
			u.errorf(c.p, where, "cost cannot be estimated: %v", err)
			return false
		}
		total = addSat(total, est.Max)
		return true
	}
	for _, l := range lets {
		if !estimate(l.ast) {
			return
		}
		switch l.ast.OutputType().Kind() {
		case types.ListKind, types.MapKind, types.StringKind:
			e := using(l.ast)
			root := l.ast.NativeRep().Expr()
			size, ok := e.selectedSize(root)
			if !ok && root.Kind() == celast.IdentKind {
				size, ok = sizes[root.AsIdent()]
			}
			if !ok {
				size = unbounded
			}
			sizes[l.name] = size
		}
	}
	if !estimate(expr) || unresolved {
		return // a type it reads did not resolve, which is reported where the type is written
	}
	const fix = "bound the lists and strings it reads with max, and give a let that holds one only a field"
	switch {
	case total == unbounded:
		u.errorf(c.p, where, "its worst case cost has no bound; %s, and compare no record that can contain itself", fix)
	case total > CostLimit:
		u.errorf(c.p, where, "its worst case costs %d units, over the limit of %d; %s", total, CostLimit, fix)
	}
}

// isBoundInput reports whether an identifier is the one a macro binds its receiver to, so that the
// receiver is read once ("@filter_input", "@sortBy_input", and the like).
func isBoundInput(name string) bool {
	return strings.HasPrefix(name, "@") && strings.HasSuffix(name, "_input")
}
