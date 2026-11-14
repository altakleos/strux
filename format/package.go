package format

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Format is the definition format this package implements.
const Format = 1

// OperationPoints are the decision points the format defines, where an operation may run. A
// package may declare others, such as a host's merge path.
var OperationPoints = []string{"verification", "evaluation", "write-path", "client"}

var (
	kebab     = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
	snake     = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	enumValue = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

// celKeywords are names no expression can use as an identifier.
var celKeywords = map[string]bool{
	"true": true, "false": true, "null": true, "in": true, "as": true, "break": true, "const": true,
	"continue": true, "else": true, "for": true, "function": true, "if": true, "import": true,
	"let": true, "loop": true, "package": true, "namespace": true, "return": true, "var": true,
	"void": true, "while": true,
}

// Package is one parsed definitions file.
type Package struct {
	Name      string
	File      string
	Content   map[string]any
	Digest    string
	Format    int64
	Revision  int64
	DependsOn []string
	Types     map[string]*Decl
	Defs      map[string]*Definition
	// Rules are the package's rules, which the rules.yaml beside its definitions states; the
	// checker sets them.
	Rules []map[string]any
	// Points holds the decision points the package declares, beyond the format's.
	Points map[string]bool
	// AnyHolds lists the literal use names that expressions of the package read with holds or
	// permitted over a reference to any definition. The checker records them while it checks the package, so a
	// version can tell whether some package it lists declares each.
	AnyHolds []HeldUse
	// invalidParts are the parts whose shape was reported as wrong, which are not reported again
	// where a use includes them.
	invalidParts map[string]bool
	errs         []string
}

// HeldUse is a use name that an expression reads with holds or permitted over a reference to any
// definition, which function it was, and where the expression lies.
type HeldUse struct{ Function, Use, Where string }

// DeclaresUse says whether some definition of the package declares a use of that name.
func (p *Package) DeclaresUse(use string) bool {
	for _, d := range p.Defs {
		if mapping(mapping(d.Raw["uses"])[use]) != nil {
			return true
		}
	}
	return false
}

// Definition is one definition of a package.
type Definition struct {
	Pkg   *Package
	Name  string
	Raw   map[string]any
	Types map[string]*Decl
	Decl  *Decl // the definition as a type; it has fields only when it has a structure
	// Inputs holds each operation's inputs, by operation name.
	Inputs map[string]*Decl
	// Outputs holds what each operation that types its result returns, by operation name.
	Outputs map[string]*Decl
}

// Decidable reports whether a fact of the definition's kind can be recognized: the kind has a `recognize`
// use, or one of its operations names a use, since every fact is decided by one use. A reference to an instance of a
// definition that is not decidable could never be satisfied.
func (d *Definition) Decidable() bool {
	if mapping(mapping(d.Raw["uses"])["recognize"]) != nil {
		return true
	}
	ops := mapping(d.Raw["operations"])
	for _, name := range sortedKeys(ops) {
		if _, ok := mapping(ops[name])["use"]; ok {
			return true
		}
	}
	return false
}

// HasStructure reports whether instances of the definition have fields.
func (d *Definition) HasStructure() bool { _, ok := d.Raw["structure"]; return ok }

// Decl is a type: a definition's structure, a declared record, variant or enumeration, or a
// built-in record.
type Decl struct {
	Builtin    bool   // a built-in record, which every definition may name
	Package    string // builtinPackage for built-in records
	Definition string // the definition it belongs to, if any
	Name       string // empty for a definition's own structure
	Kind       string // definition, record, variant, enum
	Fields     map[string]*Field
	Values     []string
}

// Field is one field specification.
type Field struct {
	Name     string
	TypeSrc  string
	Type     *Type
	Required bool
	Max      int64
	Key      string
	Enabled  bool
}

// CELName is the name of the type in expressions.
func (d *Decl) CELName() string {
	if d.Builtin {
		return builtinPackage + "." + d.Name
	}
	n := underscore(d.Package)
	if d.Definition != "" {
		n += "." + underscore(d.Definition)
	}
	if d.Name != "" {
		n += "." + underscore(d.Name)
	}
	return n
}

// Path is the type's name as definitions write it, qualified by package.
func (d *Decl) Path() string {
	if d.Builtin {
		return d.Name
	}
	n := d.Package + "/"
	switch {
	case d.Definition != "" && d.Name != "":
		return n + d.Definition + "." + d.Name
	case d.Definition != "":
		return n + d.Definition
	}
	return n + d.Name
}

func underscore(s string) string { return strings.ReplaceAll(s, "-", "_") }

// builtinName reports whether a definition or type name is taken by a built-in type or a type
// constructor.
func builtinName(n string) bool {
	return Scalars[n] != nil || builtinRecords[n] != nil || constructors[n]
}

func (p *Package) errorf(where, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if where != "" {
		msg = where + ": " + msg
	}
	p.errs = append(p.errs, p.File+": "+msg)
}

// Build reads a package's content. Errors in its shape are kept with the package; types and
// expressions are checked when the universe of packages is checked.
func Build(name, file string, content any) *Package {
	p := &Package{Name: name, File: file, Types: map[string]*Decl{}, Defs: map[string]*Definition{}, Points: map[string]bool{}}
	m, ok := content.(map[string]any)
	if !ok {
		p.errorf("", "the file must be a mapping")
		return p
	}
	p.keys("", m, []string{"format", "package", "revision", "definitions"}, "depends_on", "decision_points", "types", "parts")
	m = p.expand(m)
	p.Content = m
	p.Digest = Digest(m)
	if !kebab.MatchString(name) {
		p.errorf("package", "a package name is lowercase words joined by hyphens")
	}
	if f, ok := m["format"].(int64); !ok || f != Format {
		p.errorf("format", "must be %d", Format)
	} else {
		p.Format = f
	}
	if m["package"] != name {
		p.errorf("package", "is %v, expected %q", m["package"], name)
	}
	if r, ok := m["revision"].(int64); !ok || r < 1 {
		p.errorf("revision", "must be a positive integer")
	} else {
		p.Revision = r
	}
	for i, d := range p.listOf("depends_on", m) {
		s, ok := d.(string)
		switch {
		case !ok || !kebab.MatchString(s) || s == name:
			p.errorf(fmt.Sprintf("depends_on[%d]", i), "%v is not another package's name", d)
		case contains(p.DependsOn, s):
			p.errorf(fmt.Sprintf("depends_on[%d]", i), "%s is listed twice", s)
		default:
			p.DependsOn = append(p.DependsOn, s)
		}
	}
	points := p.mapOf("decision_points", m)
	for _, pname := range sortedKeys(points) {
		where := "decision_points." + pname
		if !kebab.MatchString(pname) {
			p.errorf(where, "a decision point's name is lowercase words joined by hyphens")
		}
		if contains(OperationPoints, pname) || pname == "conduct" {
			p.errorf(where, "%s is a decision point the format defines", pname)
		}
		pm := p.mapAt(where, points[pname])
		p.keys(where, pm, []string{"meaning"})
		p.text(where+".meaning", pm["meaning"])
		p.Points[pname] = true
	}
	types := p.mapOf("types", m)
	for _, tname := range sortedKeys(types) {
		p.Types[tname] = p.declType("types."+tname, "", tname, types[tname])
	}
	defs := p.mapOf("definitions", m)
	for _, dname := range sortedKeys(defs) {
		p.Defs[dname] = p.definition(dname, defs[dname])
		if p.Types[dname] != nil {
			p.errorf(dname, "a package type of the same name exists, so the name resolves twice")
		}
	}
	return p
}

// Errors returns the problems found in the package's shape.
func (p *Package) Errors() []string { return p.errs }

func (p *Package) keys(where string, m map[string]any, required []string, optional ...string) {
	allowed := map[string]bool{}
	for _, k := range append(append([]string{}, required...), optional...) {
		allowed[k] = true
	}
	for _, k := range required {
		if _, ok := m[k]; !ok {
			p.errorf(where, "missing %s", k)
		}
	}
	for _, k := range sortedKeys(m) {
		if !allowed[k] {
			p.errorf(where, "unknown key %s", k)
		}
	}
}

// mapOf returns m[key] as a mapping, reporting a value of another kind; absent is empty.
func (p *Package) mapOf(where string, m map[string]any) map[string]any {
	key := where[strings.LastIndex(where, ".")+1:]
	v, ok := m[key]
	if !ok {
		return map[string]any{}
	}
	mm, isMap := v.(map[string]any)
	if !isMap {
		p.errorf(where, "must be a mapping")
		return map[string]any{}
	}
	return mm
}

// listOf returns m[key] as a list, reporting a value of another kind; absent is empty.
func (p *Package) listOf(where string, m map[string]any) []any {
	key := where[strings.LastIndex(where, ".")+1:]
	v, ok := m[key]
	if !ok {
		return nil
	}
	l, isList := v.([]any)
	if !isList {
		p.errorf(where, "must be a list")
		return nil
	}
	return l
}

func (p *Package) definition(name string, raw any) *Definition {
	d := &Definition{Pkg: p, Name: name, Types: map[string]*Decl{}, Inputs: map[string]*Decl{}, Outputs: map[string]*Decl{}, Raw: map[string]any{}}
	d.Decl = &Decl{Package: p.Name, Definition: name, Kind: "definition", Fields: map[string]*Field{}}
	if !kebab.MatchString(name) {
		p.errorf(name, "a definition name is lowercase words joined by hyphens")
	}
	if builtinName(name) {
		p.errorf(name, "%s is a built-in type name", name)
	}
	m, ok := raw.(map[string]any)
	if !ok {
		p.errorf(name, "must be a mapping")
		return d
	}
	d.Raw = m
	p.keys(name, m, []string{"purpose"}, "governing", "singleton", "types", "structure", "meaning", "checks", "operations", "uses")
	p.text(name+".purpose", m["purpose"])
	for _, k := range []string{"governing", "singleton"} {
		if v, ok := m[k]; ok {
			if _, isBool := v.(bool); !isBool {
				p.errorf(name+"."+k, "must be true or false")
			}
		}
	}
	types := p.mapOf(name+".types", m)
	for _, tname := range sortedKeys(types) {
		d.Types[tname] = p.declType(name+".types."+tname, name, tname, types[tname])
	}
	if _, ok := m["structure"]; ok {
		d.Decl.Fields = p.fields(name+".structure", m["structure"])
	}
	p.meaning(name+".meaning", m["meaning"])
	checks := p.mapOf(name+".checks", m)
	p.keys(name+".checks", checks, nil, "formal", "semantic")
	formal := p.mapOf(name+".checks.formal", checks)
	for _, cname := range sortedKeys(formal) {
		where := name + ".checks.formal." + cname
		p.name(where, cname)
		cm := p.mapAt(where, formal[cname])
		p.keys(where, cm, []string{"rule", "expr", "fix"}, "let")
		p.text(where+".rule", cm["rule"])
		p.text(where+".fix", cm["fix"])
		p.lets(where+".let", cm)
	}
	semantic := p.mapOf(name+".checks.semantic", checks)
	for _, cname := range sortedKeys(semantic) {
		where := name + ".checks.semantic." + cname
		p.name(where, cname)
		if formal[cname] != nil {
			p.errorf(where, "a formal check of the same name exists")
		}
		cm := p.mapAt(where, semantic[cname])
		p.keys(where, cm, []string{"expectation", "evaluator"}, "combination")
		p.text(where+".expectation", cm["expectation"])
		p.text(where+".evaluator", cm["evaluator"])
		if v, ok := cm["combination"]; ok {
			p.combination(where+".combination", v)
		}
	}
	ops := p.mapOf(name+".operations", m)
	for _, oname := range sortedKeys(ops) {
		where := name + ".operations." + oname
		p.name(where, oname)
		om := p.mapAt(where, ops[oname])
		p.keys(where, om, []string{"at"}, "inputs", "outputs", "use", "decides", "performs", "effect", "refuses", "returns", "reports", "recovery", "abstract", "binds")
		p.binderShape(where, om)
		if at := fmt.Sprint(om["at"]); !strings.Contains(at, "/") && !contains(OperationPoints, at) && !p.Points[at] {
			p.errorf(where, "must state at: one of %s, or a decision point the package declares", strings.Join(OperationPoints, ", "))
		}
		for _, k := range []string{"effect", "refuses", "reports", "recovery"} {
			if v, ok := om[k]; ok {
				p.text(where+"."+k, v)
			}
		}
		for _, k := range []string{"use", "decides"} {
			if v, ok := om[k]; ok {
				p.name(where+"."+k, fmt.Sprint(v))
			}
		}
		for i, e := range p.listOf(where+".performs", om) {
			if _, ok := e.(string); !ok {
				p.errorf(fmt.Sprintf("%s.performs[%d]", where, i), "must be an element path")
			}
		}
		if r, ok := om["returns"]; ok {
			if l, isList := r.([]any); isList {
				for i, e := range l {
					p.text(fmt.Sprintf("%s.returns[%d]", where, i), e)
				}
			} else {
				p.text(where+".returns", r)
			}
		}
		if in, ok := om["inputs"]; ok {
			d.Inputs[oname] = &Decl{Package: p.Name, Definition: name, Name: oname + "-inputs", Kind: "record", Fields: p.fields(where+".inputs", in)}
		}
		if out, ok := om["outputs"]; ok {
			d.Outputs[oname] = &Decl{Package: p.Name, Definition: name, Name: oname + "-outputs", Kind: "record", Fields: p.fields(where+".outputs", out)}
		}
	}
	deciding := map[string]bool{"recognize": true}
	for _, o := range ops {
		if v, ok := mapping(o)["use"]; ok {
			deciding[fmt.Sprint(v)] = true
		}
	}
	uses := p.mapOf(name+".uses", m)
	for _, uname := range sortedKeys(uses) {
		where := name + ".uses." + uname
		p.name(where, uname)
		um := p.mapAt(where, uses[uname])
		p.keys(where, um, nil, "candidate", "beneficiaries", "accepts_declared", "independent", "revises", "let", "requires_satisfied", "requires")
		if _, ok := um["beneficiaries"]; !ok && !deciding[uname] {
			p.errorf(where, "decides no fact, so it must name its beneficiaries; [] states that it benefits no particular party")
		}
		if deciding[uname] {
			for _, k := range []string{"beneficiaries", "accepts_declared", "independent"} {
				if _, ok := um[k]; ok {
					msg := "decides facts, so it names no beneficiaries and what hangs on them; whoever relies on what such a fact decides is protected by the use that relies on it"
					if k == "independent" {
						msg = "decides facts, so it names no independent decision; a use that relies on what it decides asks for one"
					}
					p.errorf(where+"."+k, "%s", msg)
				}
			}
		}
		if _, ok := um["independent"]; ok {
			if _, has := um["beneficiaries"]; !has {
				p.errorf(where+".independent", "needs beneficiaries, since the decision it asks for must come from outside their reach")
			}
		}
		if v, ok := um["revises"]; ok {
			if _, isBool := v.(bool); !isBool {
				p.errorf(where+".revises", "must be true or false")
			}
		}
		p.lets(where+".let", um)
		names := map[string]bool{}
		for i, s := range p.listOf(where+".requires_satisfied", um) {
			sname, ok := s.(string)
			switch {
			case !ok:
				p.errorf(fmt.Sprintf("%s.requires_satisfied[%d]", where, i), "must be a check's name")
			case names[sname]:
				p.errorf(where+".requires_satisfied", "%s is listed twice", sname)
			default:
				names[sname] = true
			}
		}
		requires := p.mapOf(where+".requires", um)
		if len(requires) == 0 && len(names) == 0 && !deciding[uname] {
			p.errorf(where, "decides no fact and requires nothing, so it would always hold")
		}
		for _, cname := range sortedKeys(requires) {
			cwhere := where + ".requires." + cname
			p.name(cwhere, cname)
			if names[cname] {
				p.errorf(cwhere, "requires_satisfied already names a condition %s", cname)
			}
			cm := p.mapAt(cwhere, requires[cname])
			p.keys(cwhere, cm, []string{"rule"}, "expr", "check", "use", "interpreter", "of", "each", "address", "waivable")
			p.text(cwhere+".rule", cm["rule"])
			if v, ok := cm["waivable"]; ok {
				if b, isBool := v.(bool); !isBool {
					p.errorf(cwhere+".waivable", "must be true or false")
				} else if b && cm["interpreter"] != nil {
					p.errorf(cwhere+".waivable", "a condition an interpreter capability decides is a rule of verification, which is never waivable")
				}
			}
			forms := 0
			for _, k := range []string{"expr", "check", "use", "interpreter"} {
				if _, ok := cm[k]; ok {
					forms++
				}
			}
			if forms != 1 {
				p.errorf(cwhere, "needs exactly one of expr, check, use or interpreter")
			}
			if _, ok := cm["of"]; ok && cm["check"] == nil && cm["use"] == nil {
				p.errorf(cwhere, "of applies only to a check or use condition")
			}
			if _, ok := cm["address"]; ok && cm["each"] == nil {
				p.errorf(cwhere, "address applies only with each")
			}
		}
	}
	return d
}

// combination checks a semantic check's declared combination: a rule and a cap.
func (p *Package) combination(where string, v any) {
	m := p.mapAt(where, v)
	p.keys(where, m, []string{"rule", "cap"})
	if r, ok := m["rule"]; ok && !contains(CombinationRules, fmt.Sprint(r)) {
		p.errorf(where+".rule", "must be one of %s", strings.Join(CombinationRules, ", "))
	}
	if c, ok := m["cap"]; ok {
		if n, isInt := c.(int64); !isInt || n < 1 {
			p.errorf(where+".cap", "must be a positive integer")
		}
	}
}

func (p *Package) lets(where string, m map[string]any) {
	lets := p.mapOf(where, m)
	for _, lname := range sortedKeys(lets) {
		if !snake.MatchString(lname) || celKeywords[lname] {
			p.errorf(where+"."+lname, "a let name is lowercase words joined by underscores, and not a keyword")
		}
	}
}

func (p *Package) declType(where, def, name string, raw any) *Decl {
	if !kebab.MatchString(name) {
		p.errorf(where, "a type name is lowercase words joined by hyphens")
	}
	if builtinName(name) {
		p.errorf(where, "%s is a built-in type name", name)
	}
	d := &Decl{Package: p.Name, Definition: def, Name: name}
	m := p.mapAt(where, raw)
	p.keys(where, m, nil, "meaning", "fields", "one_of", "enum")
	if v, ok := m["meaning"]; ok {
		p.text(where+".meaning", v)
	}
	kinds := 0
	if f, ok := m["fields"]; ok {
		d.Kind, d.Fields = "record", p.fields(where+".fields", f)
		kinds++
		if d.Fields["validity"] != nil {
			p.errorf(where+".fields.validity", "a validity inside an embedded record is never read: verification reads the validity of a decision from the top level of the definition, so make it a field of the definition")
		}
	}
	if f, ok := m["one_of"]; ok {
		d.Kind, d.Fields = "variant", p.fields(where+".one_of", f)
		kinds++
		if d.Fields["validity"] != nil {
			p.errorf(where+".one_of.validity", "a validity inside an embedded variant is never read: verification reads the validity of a decision from the top level of the definition, so make it a field of the definition")
		}
		for _, fname := range sortedKeys(d.Fields) {
			if d.Fields[fname].Required {
				p.errorf(where+".one_of."+fname, "an alternative cannot be required")
			}
		}
	}
	if _, ok := m["enum"]; ok {
		d.Kind = "enum"
		values := p.listOf(where+".enum", m)
		if len(values) == 0 {
			p.errorf(where+".enum", "must list at least one value")
		}
		for _, v := range values {
			s, ok := v.(string)
			switch {
			case !ok || !enumValue.MatchString(s):
				p.errorf(where+".enum", "%v is not an enum value", v)
			case contains(d.Values, s):
				p.errorf(where+".enum", "%s is listed twice", s)
			default:
				d.Values = append(d.Values, s)
			}
		}
		kinds++
	}
	if kinds != 1 {
		p.errorf(where, "needs exactly one of fields, one_of or enum")
	}
	return d
}

func (p *Package) fields(where string, raw any) map[string]*Field {
	out := map[string]*Field{}
	fieldMap := p.mapAt(where, raw)
	for _, fname := range sortedKeys(fieldMap) {
		fwhere := where + "." + fname
		if !snake.MatchString(fname) || celKeywords[fname] {
			p.errorf(fwhere, "a field name is lowercase words joined by underscores, and not a keyword")
		}
		fm := p.mapAt(fwhere, fieldMap[fname])
		p.keys(fwhere, fm, []string{"type"}, "required", "meaning", "max", "key", "enabled")
		f := &Field{Name: fname, Enabled: true}
		if v, ok := fm["type"]; ok {
			if s, isString := v.(string); isString {
				f.TypeSrc = s
			} else {
				p.errorf(fwhere+".type", "must be a type, written as a string")
			}
		}
		if v, ok := fm["required"]; ok {
			b, isBool := v.(bool)
			if !isBool {
				p.errorf(fwhere+".required", "must be true or false")
			}
			f.Required = b
		}
		if v, ok := fm["enabled"]; ok {
			b, isBool := v.(bool)
			if !isBool {
				p.errorf(fwhere+".enabled", "must be true or false")
			}
			f.Enabled = b
		}
		if v, ok := fm["max"]; ok {
			n, isInt := v.(int64)
			if !isInt || n < 1 {
				p.errorf(fwhere+".max", "must be a positive integer")
			}
			f.Max = n
		}
		if v, ok := fm["key"]; ok {
			s, isString := v.(string)
			if !isString || !snake.MatchString(s) {
				p.errorf(fwhere+".key", "must name a field")
			}
			f.Key = s
		}
		if v, ok := fm["meaning"]; ok {
			p.text(fwhere+".meaning", v)
		}
		out[fname] = f
	}
	return out
}

func (p *Package) meaning(where string, raw any) {
	if raw == nil {
		return
	}
	var leaves func(string, any)
	leaves = func(w string, v any) {
		switch x := v.(type) {
		case string:
		case map[string]any:
			for _, k := range sortedKeys(x) {
				leaves(w+"."+k, x[k])
			}
		case []any:
			for i, e := range x {
				leaves(fmt.Sprintf("%s[%d]", w, i), e)
			}
		default:
			p.errorf(w, "meaning holds prose only")
		}
	}
	if _, ok := raw.(map[string]any); !ok {
		p.errorf(where, "must be a mapping")
		return
	}
	leaves(where, raw)
}

func (p *Package) name(where, n string) {
	if !kebab.MatchString(n) {
		p.errorf(where, "a name is lowercase words joined by hyphens")
	}
}

func (p *Package) text(where string, v any) {
	if s, ok := v.(string); !ok || strings.TrimSpace(s) == "" {
		p.errorf(where, "must be nonempty text")
	}
}

func (p *Package) mapAt(where string, v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		p.errorf(where, "must be a mapping")
		return map[string]any{}
	}
	return m
}

func mapping(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func contains(l []string, s string) bool {
	for _, e := range l {
		if e == s {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// binderShape checks the keys that make an operation abstract or a binder of an abstract one. An
// abstract operation describes what any binder must provide: where it runs, its inputs, and what it
// returns, refuses and reports. It has no effect of its own, so it names no use, performs nothing
// and states no recovery. A binder names the abstract operation it fills.
func (p *Package) binderShape(where string, om map[string]any) {
	abstract := false
	if v, ok := om["abstract"]; ok {
		b, isBool := v.(bool)
		if !isBool {
			p.errorf(where+".abstract", "must be true or false")
		}
		abstract = b
	}
	if abstract {
		for _, k := range []string{"effect", "use", "decides", "performs", "recovery", "binds"} {
			if _, ok := om[k]; ok {
				p.errorf(where, "an abstract operation has no %s of its own; its binder supplies it", k)
			}
		}
	}
	if v, ok := om["binds"]; ok {
		if _, isString := v.(string); !isString {
			p.errorf(where+".binds", "must be an element path")
		}
	}
}
