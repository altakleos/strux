package format

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
)

// Reference is a reference as an expression reads it: the identity of the fact that recorded a
// revision, and the content digest of that revision. Two references are equal when their digests
// are, and the digest of a reference is the one it holds, whatever it resolves to.
type Reference struct {
	Fact   string
	Digest string
}

// referenceType is the type references have at evaluation. Its name is that of the reference
// type expressions are checked against, whatever the definition referred to.
var referenceType = types.NewOpaqueType(builtinPackage + ".ref")

// ConvertToNative gives the reference itself.
func (r Reference) ConvertToNative(t reflect.Type) (any, error) {
	if t == reflect.TypeOf(r) {
		return r, nil
	}
	return nil, fmt.Errorf("a reference is no %v", t)
}

// ConvertToType converts a reference to the reference type only.
func (r Reference) ConvertToType(t ref.Type) ref.Val {
	if t.TypeName() == referenceType.TypeName() {
		return r
	}
	return types.NewErr("a reference is no %s", t.TypeName())
}

// Equal compares two references by their digests.
func (r Reference) Equal(other ref.Val) ref.Val {
	o, ok := other.(Reference)
	return types.Bool(ok && o.Digest == r.Digest)
}

// Type is the reference type.
func (r Reference) Type() ref.Type { return referenceType }

// Value is the reference itself.
func (r Reference) Value() any { return r }

// Reader answers, for one evaluation, the functions that read the log where state is read: the
// revisions recognized before that position, with those whose facts are pending or indeterminate
// as possible members.
type Reader interface {
	// Resolve gives the revision a reference names: the kind of its fact, as package/definition,
	// its content and the envelope of its fact, as they are parsed. It is false where the
	// reference resolves to nothing that is recognized.
	Resolve(r Reference) (kind string, content, envelope map[string]any, ok bool)
	// History lists the revisions of a definition, written package/definition, in log order, with
	// the indexes of those that are possible members.
	History(definition string) (revisions []Reference, possible []int)
	// InForce says whether a referenced revision is a decision in force.
	InForce(r Reference) Result
	// Recognized says whether a referenced revision is recognized.
	Recognized(r Reference) Result
	// Current gives the latest recognized revision of a referenced revision's instance.
	Current(r Reference) (Reference, Result)
	// Authorized says whether authority in force gives a principal a use.
	Authorized(principal, use string) Result
	// OutsideReach says whether no credential registered to a principal is usable from an
	// environment in the reach of any of the principals.
	OutsideReach(principal string, principals []string) Result
	// Holds decides a use of a referenced revision without exceptions, and Permitted with the
	// exceptions in force, each in its own two passes inside the pass the reader reads in.
	Holds(r Reference, use string) Result
	Permitted(r Reference, use string) Result
	// Findings lists the recognized findings of a check, the evaluator's, bound to the revision
	// with a digest, in log order, with the indexes of those that are possible members; with
	// candidate set, only those whose request names that revision as its candidate.
	Findings(check, digest string, candidate bool) (findings []Reference, possible []int)
	// Retained says whether bytes with a digest are available where state is read, and Quoted
	// whether a quote occurs at a location of them.
	Retained(digest string) Result
	Quoted(digest, location, quote string) Result
}

// UseInputs are what the conditions of a use read besides the log: the instance, the envelope of
// the fact that recorded it, the revision it supersedes, and the custody profile and trust root in
// force, each as it is parsed.
type UseInputs struct {
	Self        map[string]any
	Fact        map[string]any
	Predecessor *Reference
	Custody     map[string]any
	TrustRoot   map[string]any
}

// EvaluateUse evaluates the conditions of a use that an expression decides, with the use's lets,
// reading the log through r, and returns the result of each by name. A condition that a check,
// another use or an interpreter capability decides is left to the caller.
func (u *Universe) EvaluateUse(pkg, def, use string, in UseInputs, r Reader) (map[string]Result, error) {
	e, err := u.useEvaluation(pkg, def, use, in, r)
	if err != nil {
		return nil, err
	}
	conditions := mapping(e.node["requires"])
	asts := map[string]*cel.Ast{}
	before := len(u.errs)
	for _, name := range sortedKeys(conditions) {
		if expr, ok := mapping(conditions[name])["expr"]; ok {
			asts[name] = u.compile(e.c, expr, e.where+".requires."+name, types.BoolType)
		}
	}
	if err := u.problemsSince(before); err != nil {
		return nil, err
	}
	results := map[string]Result{}
	for name, a := range asts {
		prg, err := u.program(e.c, a)
		if err != nil {
			return nil, err
		}
		results[name] = result(prg.Eval(e.act))
	}
	return results, nil
}

// EvaluateUseList evaluates an expression of a use that is a list of strings, such as its
// beneficiaries, named by its key, in the use's context. It returns the list, or the result that
// says why there is none: a list that holds a possible member, or one that is otherwise unknown.
func (u *Universe) EvaluateUseList(pkg, def, use, key string, in UseInputs, r Reader) ([]string, Result, error) {
	e, err := u.useEvaluation(pkg, def, use, in, r)
	if err != nil {
		return nil, Result{}, err
	}
	src, ok := e.node[key]
	if !ok {
		return nil, Result{}, fmt.Errorf("%s/%s.uses.%s has no %s", pkg, def, use, key)
	}
	before := len(u.errs)
	a := u.compile(e.c, src, e.where+"."+key, types.NewListType(types.StringType))
	if err := u.problemsSince(before); err != nil {
		return nil, Result{}, err
	}
	prg, err := u.program(e.c, a)
	if err != nil {
		return nil, Result{}, err
	}
	out, _, err := prg.Eval(e.act)
	if err != nil {
		return nil, Result{"unknown", err.Error()}, nil
	}
	if types.IsUnknownOrError(out) {
		return nil, result(out, nil, nil), nil
	}
	native, err := out.ConvertToNative(reflect.TypeOf([]string{}))
	if err != nil {
		return nil, Result{"unknown", "the list is not yet decided: " + err.Error()}, nil
	}
	return native.([]string), Result{Value: "true"}, nil
}

// useEvaluation is what evaluating a use's expressions needs: its node, its context with the
// reader's functions bound and its lets compiled, and the activation that binds its variables.
type useEvaluation struct {
	node  map[string]any
	c     *context
	act   *lazy
	where string
}

func (u *Universe) useEvaluation(pkg, def, use string, in UseInputs, r Reader) (*useEvaluation, error) {
	p := u.Packages[pkg]
	if p == nil || p.Defs[def] == nil {
		return nil, fmt.Errorf("no definition %s/%s", pkg, def)
	}
	d := p.Defs[def]
	node := mapping(mapping(d.Raw["uses"])[use])
	if node == nil {
		return nil, fmt.Errorf("%s/%s declares no use %s", pkg, def, use)
	}
	revises, _ := node["revises"].(bool)
	c, err := u.useContext(p, d, revises)
	if err != nil {
		return nil, err
	}
	env, err := c.env.Extend(u.readerFunctions(r)...)
	if err != nil {
		return nil, err
	}
	c = &context{p: c.p, d: c.d, env: env}
	where := def + ".uses." + use
	before := len(u.errs)
	c, letAsts := u.lets(c, node["let"], where)
	if err := u.problemsSince(before); err != nil {
		return nil, err
	}
	act := &lazy{programs: map[string]cel.Program{}, memo: map[string]ref.Val{}, vars: map[string]any{}}
	if d.HasStructure() && in.Self != nil {
		self, _ := u.recordValue(d.Decl, in.Self).(map[string]any)
		act.self = self
	}
	act.vars["fact"] = u.envelopeValue(in.Fact)
	if in.Predecessor != nil {
		act.vars["predecessor"] = *in.Predecessor
	}
	if kernel := u.Packages[u.Kernel]; kernel != nil {
		if cp := kernel.Defs[custodyDefinition]; cp != nil && in.Custody != nil {
			act.vars["custody"] = u.recordValue(cp.Decl, in.Custody)
		}
		if tr := kernel.Defs[trustRootDefinition]; tr != nil && in.TrustRoot != nil {
			act.vars["trust_root"] = u.recordValue(tr.Decl, in.TrustRoot)
		}
	}
	for _, l := range letAsts {
		prg, err := u.program(c, l.ast)
		if err != nil {
			return nil, err
		}
		act.programs[l.name] = prg
	}
	return &useEvaluation{node: node, c: c, act: act, where: where}, nil
}

// problemsSince takes back the problems reported since a point, as one error.
func (u *Universe) problemsSince(before int) error {
	if len(u.errs) <= before {
		return nil
	}
	problems := strings.Join(u.errs[before:], "; ")
	u.errs = u.errs[:before]
	return errors.New(problems)
}

// readerFunctions binds the functions that read references and the history to r. Each keeps the
// overload the library declares, so what type-checks is unchanged.
func (u *Universe) readerFunctions(r Reader) []cel.EnvOption {
	resolve := func(v ref.Val) (string, map[string]any, map[string]any, ref.Val) {
		reference, ok := v.(Reference)
		if !ok {
			return "", nil, nil, types.NewErr("not a reference")
		}
		kind, content, envelope, ok := r.Resolve(reference)
		if !ok {
			return "", nil, nil, undecided("the reference resolves to no revision that is recognized")
		}
		return kind, content, envelope, nil
	}
	return []cel.EnvOption{
		cel.Function("content", cel.MemberOverload("ref_content", []*types.Type{refOfT}, typeParam, cel.UnaryBinding(func(v ref.Val) ref.Val {
			kind, content, _, failed := resolve(v)
			if failed != nil {
				return failed
			}
			return types.DefaultTypeAdapter.NativeToValue(u.contentValue(kind, content))
		}))),
		cel.Function("fact", cel.MemberOverload("ref_fact", []*types.Type{refOfT}, factType, cel.UnaryBinding(func(v ref.Val) ref.Val {
			_, _, envelope, failed := resolve(v)
			if failed != nil {
				return failed
			}
			return types.DefaultTypeAdapter.NativeToValue(u.envelopeValue(envelope))
		}))),
		cel.Function("digest", cel.MemberOverload("ref_digest", []*types.Type{refOfT}, types.StringType, cel.UnaryBinding(func(v ref.Val) ref.Val {
			reference, ok := v.(Reference)
			if !ok {
				return types.NewErr("not a reference")
			}
			return types.String(reference.Digest)
		}))),
		cel.Function("history", cel.Overload("history_type", []*types.Type{types.NewTypeTypeWithParam(typeParam)}, types.NewListType(refOfT), cel.UnaryBinding(func(v ref.Val) ref.Val {
			t, ok := v.(*types.Type)
			if !ok {
				return types.NewErr("history reads a definition")
			}
			decl := u.decls[t.TypeName()]
			if decl == nil || decl.Definition == "" {
				return types.NewErr("history reads a definition, and %s is none", t.TypeName())
			}
			revisions, possible := r.History(decl.Package + "/" + decl.Definition)
			items := make([]any, len(revisions))
			for i, x := range revisions {
				items[i] = x
			}
			list, err := PossibleList(items, possible)
			if err != nil {
				return types.WrapErr(err)
			}
			return types.DefaultTypeAdapter.NativeToValue(list)
		}))),
		cel.Function("in_force", cel.Overload("in_force_ref", []*types.Type{refOfT}, types.BoolType, cel.UnaryBinding(func(v ref.Val) ref.Val {
			reference, ok := v.(Reference)
			if !ok {
				return types.NewErr("not a reference")
			}
			return boolOf(r.InForce(reference))
		}))),
		cel.Function("recognized", cel.Overload("recognized_ref", []*types.Type{refOfT}, types.BoolType, cel.UnaryBinding(func(v ref.Val) ref.Val {
			reference, ok := v.(Reference)
			if !ok {
				return types.NewErr("not a reference")
			}
			return boolOf(r.Recognized(reference))
		}))),
		cel.Function("current", cel.Overload("current_ref", []*types.Type{refOfT}, refOfT, cel.UnaryBinding(func(v ref.Val) ref.Val {
			reference, ok := v.(Reference)
			if !ok {
				return types.NewErr("not a reference")
			}
			latest, res := r.Current(reference)
			if res.Value != "true" {
				return undecided("%s", res.Reason)
			}
			return latest
		}))),
		cel.Function("authorized", cel.Overload("authorized_string_string", []*types.Type{types.StringType, types.StringType}, types.BoolType, cel.BinaryBinding(func(p, use ref.Val) ref.Val {
			return boolOf(r.Authorized(string(p.(types.String)), string(use.(types.String))))
		}))),
		cel.Function("outside_reach", cel.Overload("outside_reach_string_list", []*types.Type{types.StringType, stringList}, types.BoolType, cel.BinaryBinding(func(p, others ref.Val) ref.Val {
			native, err := others.ConvertToNative(reflect.TypeFor[[]string]())
			if err != nil {
				return types.WrapErr(err)
			}
			return boolOf(r.OutsideReach(string(p.(types.String)), native.([]string)))
		}))),
		cel.Function("holds", cel.Overload("holds_ref_string", []*types.Type{refOfT, types.StringType}, types.BoolType, cel.BinaryBinding(func(v, use ref.Val) ref.Val {
			reference, ok := v.(Reference)
			if !ok {
				return types.NewErr("not a reference")
			}
			return boolOf(r.Holds(reference, string(use.(types.String))))
		}))),
		cel.Function("permitted", cel.Overload("permitted_ref_string", []*types.Type{refOfT, types.StringType}, types.BoolType, cel.BinaryBinding(func(v, use ref.Val) ref.Val {
			reference, ok := v.(Reference)
			if !ok {
				return types.NewErr("not a reference")
			}
			return boolOf(r.Permitted(reference, string(use.(types.String))))
		}))),
		u.findingsBinding("findings", false, r),
		u.findingsBinding("candidate_findings", true, r),
		cel.Function("retained", cel.Overload("retained_string", []*types.Type{types.StringType}, types.BoolType, cel.UnaryBinding(func(d ref.Val) ref.Val {
			return boolOf(r.Retained(string(d.(types.String))))
		}))),
		cel.Function("quoted", cel.Overload("quoted_string_string_string", []*types.Type{types.StringType, types.StringType, types.StringType}, types.BoolType, cel.FunctionBinding(func(args ...ref.Val) ref.Val {
			return boolOf(r.Quoted(string(args[0].(types.String)), string(args[1].(types.String)), string(args[2].(types.String))))
		}))),
	}
}

// findingsBinding binds findings, or candidate_findings, to r, with the overload the universe
// declares for it.
func (u *Universe) findingsBinding(name string, candidate bool, r Reader) cel.EnvOption {
	return cel.Function(name, cel.Overload(name+"_string_string", []*types.Type{types.StringType, types.StringType}, u.findingsList(), cel.BinaryBinding(func(check, digest ref.Val) ref.Val {
		found, possible := r.Findings(string(check.(types.String)), string(digest.(types.String)), candidate)
		items := make([]any, len(found))
		for i, x := range found {
			items[i] = x
		}
		list, err := PossibleList(items, possible)
		if err != nil {
			return types.WrapErr(err)
		}
		return types.DefaultTypeAdapter.NativeToValue(list)
	})))
}

// boolOf gives a result as an expression's value: true, false, or unknown with its reason.
func boolOf(res Result) ref.Val {
	switch res.Value {
	case "true":
		return types.True
	case "false":
		return types.False
	}
	return undecided("%s", res.Reason)
}

// contentValue converts the content of a revision of the named kind into the value an expression
// reads.
func (u *Universe) contentValue(kind string, content map[string]any) any {
	pkg, def, _ := strings.Cut(kind, "/")
	if p := u.Packages[pkg]; p != nil && p.Defs[def] != nil && p.Defs[def].HasStructure() {
		return u.recordValue(p.Defs[def].Decl, content)
	}
	return content
}

// envelopeValue converts the envelope of a fact into the value an expression reads.
func (u *Universe) envelopeValue(envelope map[string]any) any {
	if envelope == nil {
		return nil
	}
	return u.recordValue(builtinRecords["fact"], envelope)
}

// recordValue converts a value of a record, a variant or a definition's structure, held as it is
// parsed, into the value an expression reads: a reference becomes a Reference and a time a time,
// field by field.
func (u *Universe) recordValue(d *Decl, v any) any {
	m, ok := v.(map[string]any)
	if !ok || d == nil {
		return v
	}
	out := make(map[string]any, len(m))
	for k, x := range m {
		if f := d.Fields[k]; f != nil {
			out[k] = u.typedValue(f.Type, x)
		} else {
			out[k] = x
		}
	}
	return out
}

func (u *Universe) typedValue(t *Type, v any) any {
	if t == nil || v == nil {
		return v
	}
	switch t.Kind {
	case "scalar":
		if s, ok := v.(string); ok && t.Name == "time" {
			if parsed, err := ParseTime(s); err == nil {
				return parsed
			}
		}
	case "ref":
		if m, ok := v.(map[string]any); ok {
			fact, _ := m["fact"].(string)
			digest, _ := m["digest"].(string)
			return Reference{Fact: fact, Digest: digest}
		}
	case "list":
		if l, ok := v.([]any); ok {
			out := make([]any, len(l))
			for i, x := range l {
				out[i] = u.typedValue(t.Elem, x)
			}
			return out
		}
	case "map":
		if m, ok := v.(map[string]any); ok {
			out := make(map[string]any, len(m))
			for k, x := range m {
				out[k] = u.typedValue(t.Elem, x)
			}
			return out
		}
	case "named":
		if t.Decl != nil && t.Decl.Kind != "enum" {
			return u.recordValue(t.Decl, v)
		}
	}
	return v
}
