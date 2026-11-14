package verify

import (
	"sort"
	"strings"

	"github.com/altakleos/strux/format"
)

// useConditions decides the conditions of the use that decides a fact, at the fact's position, and
// gives the names of those that fail and of those that are unknown, in the form a result names them.
func (r *run) useConditions(f *fact, use string) (outcome, []string, []string) {
	node := mapOf(mapOf(r.definition(f)["uses"])[use])
	requires := requirements(f.kind, r.definition(f), node)
	if len(requires) == 0 {
		return holds, nil, nil
	}
	in := format.UseInputs{Self: f.content, Fact: r.envelope(f), Custody: r.custodyAt(f.pos), TrustRoot: r.root.Raw}
	if f.bootstrap && in.Custody == nil {
		in.Custody = r.bootstrapCustody
	}
	if expected := str(f.p["expected"]); expected != "" {
		if pred := r.factByIdentity(expected, f.pos); pred != nil {
			in.Predecessor = &format.Reference{Fact: expected, Digest: str(pred.p["content"])}
		}
	}
	results, err := r.universe.EvaluateUse(f.pkg, f.def, use, in, reader{r: r, pos: f.pos, bootstrap: f.bootstrap})
	if err != nil {
		return limited("interpretation"), nil, []string{f.kind + ".uses." + use}
	}
	var failed, unknown, limits []string
	for _, name := range sortedKeys(requires) {
		cond := mapOf(requires[name])
		var o outcome
		switch {
		case cond["expr"] != nil:
			o = fromResult(results[name])
		case cond["check"] != nil:
			o = r.check(f, str(cond["check"]))
		case cond["interpreter"] != nil:
			o = r.capability(f, str(cond["interpreter"]))
		default:
			o = limited("interpretation")
		}
		path := f.kind + ".uses." + use + ".requires." + name
		switch o.value {
		case "false":
			failed = append(failed, path)
		case "unknown":
			unknown = append(unknown, path)
			if o.limit != "" {
				limits = append(limits, o.limit)
			}
		}
	}
	switch {
	case len(failed) > 0:
		return fails, failed, unknown
	case len(unknown) > 0 && len(limits) > 0:
		return limited(limits[0]), nil, unknown
	case len(unknown) > 0:
		return waits(), nil, unknown
	}
	return holds, nil, nil
}

func fromResult(res format.Result) outcome {
	switch res.Value {
	case "true":
		return holds
	case "false":
		return fails
	}
	return waits()
}

// check decides a check condition: a formal check on the instance itself. A semantic check, which
// the evaluator's findings decide, is unknown here.
func (r *run) check(f *fact, path string) outcome {
	pkg, rest, _ := strings.Cut(path, "/")
	def, check, ok := strings.Cut(rest, ".checks.formal.")
	if !ok {
		return waits()
	}
	p := r.universe.Packages[pkg]
	if p == nil || p.Defs[def] == nil {
		return fails
	}
	node := mapOf(mapOf(mapOf(p.Defs[def].Raw["checks"])["formal"])[check])
	res, problems := r.universe.EvaluateCheck(pkg, def, mapOf(node["let"]), str(node["expr"]), f.content)
	if len(problems) > 0 {
		return limited("interpretation")
	}
	return fromResult(res)
}

// capability decides a condition that an interpreter capability decides.
func (r *run) capability(f *fact, name string) outcome {
	switch name {
	case "verify.trust-root":
		if f.trustRootAct {
			return holds
		}
		return fails
	case "verify.definitions":
		return r.wellFormed(f)
	case "verify.package":
		return r.packagesConform(f)
	}
	return limited("interpretation")
}

// wellFormed: a package revision's entry captures a well-formed definitions file with the digest it
// records, stating the name and revision it records.
func (r *run) wellFormed(f *fact) outcome {
	bytes, ok := r.log.Blobs[str(f.content["bytes"])]
	if !ok {
		return limited("absent-bytes")
	}
	parsed, err := format.Parse(bytes)
	if err != nil {
		return fails
	}
	name := str(f.content["name"])
	p := format.Build(name, name, parsed)
	revision, _ := f.content["revision"].(int64)
	if len(p.Errors()) > 0 || p.Digest != f.content["digest"] || p.Revision != revision {
		return fails
	}
	return holds
}

// packagesConform: every package revision a configuration pins was recognized before it, the bootstrap
// configuration counting the bootstrap's package revisions wherever they sit; they conform together;
// and the kernel among them is the one the bootstrap configuration pins, by digest.
func (r *run) packagesConform(f *fact) outcome {
	pkgs := map[string]*format.Package{}
	for _, item := range list(f.content["packages"]) {
		ref := mapOf(item)
		g := r.factByIdentity(str(ref["fact"]), len(r.facts))
		if g == nil || g.kind != kernelPackage+"/package-revision" || g.content == nil || str(g.p["content"]) != str(ref["digest"]) {
			return fails
		}
		bootstrapPair := g.bootstrap && f.bootstrap
		if !bootstrapPair && (g.pos >= f.pos || g.status != Recognized) {
			return fails
		}
		bytes, ok := r.log.Blobs[str(g.content["bytes"])]
		if !ok {
			return limited("absent-bytes")
		}
		parsed, err := format.Parse(bytes)
		if err != nil {
			return fails
		}
		name := str(g.content["name"])
		pkgs[name] = format.Build(name, name, parsed)
		if name == kernelPackage && pkgs[name].Digest != r.kernel.digest {
			return fails
		}
	}
	if pkgs[kernelPackage] == nil || len(format.Check(pkgs, format.Kernel(kernelPackage))) > 0 {
		return fails
	}
	return holds
}

// envelope is the envelope of a fact, as a condition reads it.
func (r *run) envelope(f *fact) map[string]any {
	e := map[string]any{
		"kind":            f.kind,
		"log":             f.p["log"],
		"package":         f.p["package"],
		"digest":          f.entry.Fact.Identity,
		"instance":        f.instance,
		"revision_digest": f.p["content"],
		"position":        int64(f.pos),
	}
	if f.signer != "" {
		e["signer"] = f.signer
	}
	for from, to := range map[string]string{"key": "credential", "expected": "expected", "operation": "operation", "operation_id": "operation_id", "under": "under", "append_by": "append_by", "configuration": "configuration"} {
		if v, ok := f.p[from]; ok {
			e[to] = v
		}
	}
	return e
}

// reader answers the functions that read the log for a condition decided at a position: the
// facts recognized before it, with those pending or indeterminate as possible members. In the
// bootstrap, the bootstrap's package revisions, custody profile and configuration count wherever
// they sit.
type reader struct {
	r         *run
	pos       int
	bootstrap bool
}

func (x reader) visible(g *fact) (member, possible bool) {
	if g == nil {
		return false, false
	}
	if x.bootstrap && g.bootstrap && (g.kind == kernelPackage+"/package-revision" || g.kind == kernelPackage+"/custody-profile" || g.kind == kernelPackage+"/configuration") {
		return true, false
	}
	if g.pos >= x.pos {
		return false, false
	}
	switch g.status {
	case Recognized:
		return true, false
	case Pending, Indeterminate:
		return false, true
	}
	return false, false
}

func (x reader) Resolve(ref format.Reference) (string, map[string]any, map[string]any, bool) {
	for _, g := range x.r.facts {
		if g == nil || g.entry.Fact.Identity != ref.Fact {
			continue
		}
		member, possible := x.visible(g)
		if (!member && !possible) || g.content == nil || str(g.p["content"]) != ref.Digest {
			return "", nil, nil, false
		}
		return g.kind, g.content, x.r.envelope(g), true
	}
	return "", nil, nil, false
}

func (x reader) History(definition string) ([]format.Reference, []int) {
	var refs []format.Reference
	var possible []int
	for _, g := range x.r.facts {
		if g == nil || g.kind != definition {
			continue
		}
		member, maybe := x.visible(g)
		if !member && !maybe {
			continue
		}
		if maybe {
			possible = append(possible, len(refs))
		}
		refs = append(refs, format.Reference{Fact: g.entry.Fact.Identity, Digest: str(g.p["content"])})
	}
	return refs, possible
}

func (x reader) InForce(ref format.Reference) format.Result {
	for _, g := range x.r.facts {
		if g != nil && g.entry.Fact.Identity == ref.Fact {
			switch o := x.r.inForce(g, x.pos); o.value {
			case "true":
				return format.Result{Value: "true"}
			case "false":
				return format.Result{Value: "false"}
			}
			return format.Result{Value: "unknown", Reason: "whether it is in force is not decided yet"}
		}
	}
	return format.Result{Value: "false"}
}

func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// statusLists are the five lists of a result: every entry read, by the status it has.
type statusLists struct {
	Recognized    []int64
	NotRecognized []*fact
	Pending       []*fact
	Indeterminate []*fact
	Invalid       []Entry
}

func (r *run) lists() statusLists {
	var s statusLists
	for i, e := range r.entries {
		f := r.facts[i]
		switch {
		case f == nil:
			s.Invalid = append(s.Invalid, e)
		case f.status == Recognized:
			s.Recognized = append(s.Recognized, int64(i))
		case f.status == NotRecognized:
			s.NotRecognized = append(s.NotRecognized, f)
		case f.status == Pending:
			s.Pending = append(s.Pending, f)
		case f.status == Indeterminate:
			s.Indeterminate = append(s.Indeterminate, f)
		}
	}
	sort.Slice(s.Recognized, func(i, j int) bool { return s.Recognized[i] < s.Recognized[j] })
	return s
}

// requirements are the conditions a use requires: those it writes, and one for each check that it
// requires satisfied, named by the check, which the check decides.
func requirements(kind string, def, node map[string]any) map[string]any {
	out := map[string]any{}
	for name, cond := range mapOf(node["requires"]) {
		out[name] = cond
	}
	checks := mapOf(def["checks"])
	for _, item := range list(node["requires_satisfied"]) {
		name := str(item)
		for _, family := range []string{"formal", "semantic"} {
			if mapOf(checks[family])[name] != nil {
				out[name] = map[string]any{"check": kind + ".checks." + family + "." + name}
			}
		}
	}
	return out
}
