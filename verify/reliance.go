package verify

import (
	"sort"
	"strings"

	"github.com/altakleos/strux/format"
)

// RelianceResult is whether the use relied on holds for its subject, and what decided it.
type RelianceResult struct {
	Status        string            `json:"status"`
	Candidate     string            `json:"candidate,omitempty"`
	Subject       string            `json:"subject,omitempty"`
	Beneficiaries []string          `json:"beneficiaries,omitempty"`
	Conditions    []ConditionResult `json:"conditions"`
	SetAside      []int64           `json:"set_aside"`
	SecondOnly    []int64           `json:"second_only"`
	FirstOnly     []int64           `json:"first_only"`
	Exceptions    []int64           `json:"exceptions"`
	Limits        []string          `json:"limits"`
}

// ConditionResult is one condition of the use relied on, in each pass, with the exceptions that
// cover it in a pass where it is not satisfied.
type ConditionResult struct {
	Condition       string  `json:"condition"`
	First           string  `json:"first"`
	Second          string  `json:"second"`
	FirstCoveredBy  []int64 `json:"first_covered_by,omitempty"`
	SecondCoveredBy []int64 `json:"second_covered_by,omitempty"`
}

// rely decides the use relied on, written package/definition.uses.name, for the revision the
// subject names, with its state read before position at.
func (r *run) rely(use string, subject format.Reference, at int) RelianceResult {
	res := RelianceResult{Conditions: []ConditionResult{}, SetAside: []int64{}, SecondOnly: []int64{}, FirstOnly: []int64{}, Exceptions: []int64{}, Limits: []string{}}
	kindPath, name, _ := strings.Cut(use, ".uses.")
	pkg, def, _ := strings.Cut(kindPath, "/")
	subj := r.factByIdentity(subject.Fact, at)
	if subj == nil {
		res.Status = "unsatisfied"
		return res
	}
	res.Candidate = subject.Digest
	res.Subject = map[Status]string{Recognized: "recognized", NotRecognized: "not-recognized", Pending: "pending", Indeterminate: "indeterminate"}[subj.status]
	switch {
	case r.readResult.Behind:
		res.Status, res.Limits = "indeterminate", []string{"read-behind"}
		return res
	case r.readResult.BreakAfter != nil && int64(at) > *r.readResult.BreakAfter+1:
		res.Status = "pending"
		return res
	}
	in := r.useInputs(subj, name, at)
	beneficiaries, known, err := r.universe.EvaluateUseList(pkg, def, name, "beneficiaries", in, reader{r: r, pos: at})
	if err != nil || known.Value != "true" {
		res.Status, res.Limits = "indeterminate", []string{"interpretation"}
		return res
	}
	sort.Strings(beneficiaries)
	beneficiaries = dedupe(beneficiaries)
	res.Beneficiaries = beneficiaries
	second, setAside := r.secondPass(beneficiaries, subj, at)
	node := mapOf(mapOf(r.definition(subj)["uses"])[name])
	firstResults := r.passConditions(subj, pkg, def, name, node, at, subject.Digest)
	secondResults := second.passConditions(subj, pkg, def, name, node, at, subject.Digest)
	holdsAll, failed, limits := subj.status == Recognized, subj.status == NotRecognized, []string{}
	var exceptions []int64
	for _, cond := range sortedKeys(firstResults) {
		c := ConditionResult{Condition: kindPath + ".uses." + name + ".requires." + cond, First: word(firstResults[cond].value), Second: word(secondResults[cond].value)}
		waivable, _ := mapOf(requirements(subj.kind, r.definition(subj), node)[cond])["waivable"].(bool)
		for _, pass := range []struct {
			run     *run
			o       outcome
			covered *[]int64
		}{{r, firstResults[cond], &c.FirstCoveredBy}, {second, secondResults[cond], &c.SecondCoveredBy}} {
			if pass.o.value == "true" {
				continue
			}
			if waivable {
				if e, ok := pass.run.coveringException(use, subject.Digest, cond, at); ok {
					*pass.covered = []int64{int64(e)}
					exceptions = append(exceptions, int64(e))
					continue
				}
			}
			holdsAll = false
			switch pass.o.value {
			case "false":
				failed = true
			default:
				if pass.o.limit != "" {
					limits = append(limits, pass.o.limit)
				}
			}
		}
		res.Conditions = append(res.Conditions, c)
	}
	for i := 0; i < at && i < len(r.facts); i++ {
		f, g := r.facts[i], second.facts[i]
		if f == nil {
			continue
		}
		switch {
		case setAside[i]:
			if f.status != NotRecognized {
				res.SetAside = append(res.SetAside, int64(i))
			}
		case f.status == Recognized && (g == nil || g.status != Recognized):
			res.FirstOnly = append(res.FirstOnly, int64(i))
		case f.status != Recognized && g != nil && g.status == Recognized:
			res.SecondOnly = append(res.SecondOnly, int64(i))
		}
	}
	_, upperClosed := r.upperBound(int64(at - 1))
	switch {
	case holdsAll:
		res.Status = "holds"
		sort.Slice(exceptions, func(i, j int) bool { return exceptions[i] < exceptions[j] })
		res.Exceptions = dedupeInts(exceptions)
	case failed:
		res.Status = "unsatisfied"
	case len(limits) > 0:
		res.Status, res.Limits = "indeterminate", orderedLimits(limits)
	case !upperClosed:
		res.Status = "pending"
	default:
		res.Status = "unsatisfied"
	}
	return res
}

func word(v string) string {
	return map[string]string{"true": "satisfied", "false": "unsatisfied"}[v] + map[string]string{"unknown": "unknown"}[v]
}

// useInputs are what the conditions of the use relied on read: the subject, its fact, the revision
// it supersedes where the use decides revisions, and the custody profile and trust root in force.
func (r *run) useInputs(subj *fact, use string, at int) format.UseInputs {
	in := format.UseInputs{Self: subj.content, Fact: r.envelope(subj), Custody: r.custodyAt(at), TrustRoot: r.root.Raw}
	node := mapOf(mapOf(r.definition(subj)["uses"])[use])
	if revises, _ := node["revises"].(bool); revises {
		if expected := str(subj.p["expected"]); expected != "" {
			if pred := r.factByIdentity(expected, subj.pos); pred != nil {
				in.Predecessor = &format.Reference{Fact: expected, Digest: str(pred.p["content"])}
			}
		}
	}
	return in
}

// passConditions decides each condition of the use relied on, over what the pass recognizes
// before position at.
func (r *run) passConditions(subj *fact, pkg, def, use string, node map[string]any, at int, candidate string) map[string]outcome {
	out := map[string]outcome{}
	requires := requirements(subj.kind, r.definition(subj), node)
	results, err := r.universe.EvaluateUse(pkg, def, use, r.useInputs(subj, use, at), reader{r: r, pos: at})
	for _, name := range sortedKeys(requires) {
		cond := mapOf(requires[name])
		switch {
		case err != nil:
			out[name] = limited("interpretation")
		case cond["expr"] != nil:
			out[name] = fromResult(results[name])
		case strings.Contains(str(cond["check"]), ".checks.semantic."):
			out[name] = r.semantic(subj, str(cond["check"]), at, candidate)
		case cond["check"] != nil:
			out[name] = r.check(subj, str(cond["check"]))
		case cond["interpreter"] != nil:
			out[name] = r.capability(subj, str(cond["interpreter"]))
		default:
			out[name] = limited("interpretation")
		}
	}
	return out
}

// semantic decides a semantic check for a revision: the findings whose request names its content
// as the candidate, combined under the check's combination, with the cap counted over the findings
// on every revision of its instance in log order. While a pending or indeterminate finding lies
// among those the cap takes, the check is unknown, and without a finding that counts it is unknown.
func (r *run) semantic(subj *fact, path string, at int, candidate string) outcome {
	pkg, rest, _ := strings.Cut(path, "/")
	def, check, _ := strings.Cut(rest, ".checks.semantic.")
	p := r.universe.Packages[pkg]
	if p == nil || p.Defs[def] == nil {
		return fails
	}
	combination := mapOf(mapOf(mapOf(mapOf(p.Defs[def].Raw["checks"])["semantic"])[check])["combination"])
	cap64, _ := combination["cap"].(int64)
	rule := str(combination["rule"])
	var taken []*fact
	for _, f := range r.facts[:min(at, len(r.facts))] {
		if f == nil || f.kind != kernelPackage+"/finding" || f.content == nil || f.content["check"] != path {
			continue
		}
		if f.status != Recognized && f.status != Pending && f.status != Indeterminate {
			continue
		}
		request := r.requestOf(f)
		if request == nil {
			continue
		}
		about := r.factByIdentity(str(mapOf(request.content["candidate"])["fact"]), f.pos)
		if about == nil || about.instance != subj.instance {
			continue
		}
		taken = append(taken, f)
	}
	if int64(len(taken)) > cap64 {
		taken = taken[:cap64]
	}
	var results []string
	for _, f := range taken {
		if f.status != Recognized {
			return waits()
		}
		request := r.requestOf(f)
		if str(mapOf(request.content["candidate"])["digest"]) == candidate {
			results = append(results, str(f.content["result"]))
		}
	}
	return combine(rule, results)
}

// requestOf is the request a finding answers, which resolves through the finding in every pass.
func (r *run) requestOf(finding *fact) *fact {
	return r.factByIdentity(str(mapOf(finding.content["request"])["fact"]), finding.pos)
}

// combine combines findings under a combination rule: none is unknown.
func combine(rule string, results []string) outcome {
	if len(results) == 0 {
		return waits()
	}
	satisfied, unsatisfied := 0, 0
	for _, res := range results {
		switch res {
		case "satisfied":
			satisfied++
		case "unsatisfied":
			unsatisfied++
		}
	}
	n := len(results)
	switch rule {
	case "unanimous":
		switch {
		case satisfied == n:
			return holds
		case unsatisfied == n:
			return fails
		}
	case "all-satisfied":
		switch {
		case satisfied == n:
			return holds
		case unsatisfied > 0:
			return fails
		}
	case "majority":
		switch {
		case 2*satisfied > n:
			return holds
		case 2*unsatisfied >= n:
			return fails
		}
	}
	return waits()
}

// coveringException is the earliest exception, recognized in this pass before position at and in
// force there, that names the use, the candidate and the condition.
func (r *run) coveringException(use, candidate, condition string, at int) (int, bool) {
	for _, f := range r.facts[:min(at, len(r.facts))] {
		if f == nil || f.kind != kernelPackage+"/exception" || f.status != Recognized || f.content == nil {
			continue
		}
		if f.content["use"] != use || f.content["candidate"] != candidate || f.content["condition"] != condition {
			continue
		}
		if r.inForce(f, at).value == "true" {
			return f.pos, true
		}
	}
	return 0, false
}

// secondPass decides every fact before position at again, with the facts signed from within the
// reach of a beneficiary other than the owner set aside. It keeps the trust-root acts, the subject
// and its predecessor, and the findings signed from outside every beneficiary's reach, with their
// status. A fact decided again also falls where an exact reference in its content names a fact
// that the first pass recognizes and this one does not.
func (r *run) secondPass(beneficiaries []string, subj *fact, at int) (*run, map[int]bool) {
	reach := map[string]bool{}
	others := map[string]bool{}
	for _, b := range beneficiaries {
		if b != r.root.Owner {
			others[b] = true
		}
	}
	for _, f := range r.facts[:min(at, len(r.facts))] {
		if f != nil && f.kind == kernelPackage+"/credential-registration" && f.status == Recognized && others[str(f.content["principal"])] {
			reach[str(f.content["environment"])] = true
		}
	}
	within := func(f *fact) bool {
		if others[f.signer] {
			return true
		}
		if key := str(f.p["key"]); key != "" {
			if registration, o := r.registrationOf(key, f.pos); o.value == "true" {
				return reach[str(registration.content["environment"])]
			}
		}
		return false
	}
	second := *r
	second.facts = make([]*fact, len(r.facts))
	setAside := map[int]bool{}
	predecessor := str(subj.p["expected"])
	for i := 0; i < at && i < len(r.facts); i++ {
		f := r.facts[i]
		switch {
		case f == nil:
		case f.trustRootAct || f == subj || f.entry.Fact.Identity == predecessor:
			second.facts[i] = f
		case f.kind == kernelPackage+"/finding" && !within(f):
			second.facts[i] = f
		case within(f):
			kept := *f
			kept.status = NotRecognized
			second.facts[i] = &kept
			setAside[i] = true
		default:
			g := &fact{pos: f.pos, entry: f.entry, p: f.p, kind: f.kind, pkg: f.pkg, def: f.def, content: f.content, bootstrap: f.bootstrap, trustRootAct: f.trustRootAct, instance: f.instance}
			second.decide(g)
			if g.status == Recognized {
				r.fallsWithWhatItCites(&second, g)
			}
			second.facts[i] = g
		}
	}
	return &second, setAside
}

// fallsWithWhatItCites makes a fact decided again in the second pass fall where its content names
// by exact reference an earlier fact that the first pass recognizes and the second does not.
func (r *run) fallsWithWhatItCites(second *run, g *fact) {
	for _, ref := range references(g.content) {
		target := r.factByIdentity(ref, g.pos)
		if target == nil || target.status != Recognized {
			continue
		}
		switch t := second.facts[target.pos]; {
		case t == nil || t.status == NotRecognized:
			g.status = NotRecognized
			return
		case t.status == Pending:
			g.status = Pending
		case t.status == Indeterminate:
			g.status = Indeterminate
		}
	}
}

// references lists the identities of the facts that the references in content name.
func references(v any) []string {
	var out []string
	switch x := v.(type) {
	case map[string]any:
		fact, isFact := x["fact"].(string)
		_, isDigest := x["digest"].(string)
		if isFact && isDigest && len(x) == 2 {
			return []string{fact}
		}
		for _, k := range sortedKeys(x) {
			out = append(out, references(x[k])...)
		}
	case []any:
		for _, y := range x {
			out = append(out, references(y)...)
		}
	}
	return out
}

func dedupe(s []string) []string {
	var out []string
	for i, x := range s {
		if i == 0 || x != s[i-1] {
			out = append(out, x)
		}
	}
	return out
}

func dedupeInts(s []int64) []int64 {
	out := []int64{}
	for i, x := range s {
		if i == 0 || x != s[i-1] {
			out = append(out, x)
		}
	}
	return out
}
