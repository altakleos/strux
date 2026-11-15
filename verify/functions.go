package verify

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/altakleos/strux/format"
)

// The functions of the library that read the log beyond a reference and the history, as the
// reader of a pass answers them at the position where state is read.

func decided(v bool) format.Result {
	if v {
		return format.Result{Value: "true"}
	}
	return format.Result{Value: "false"}
}

func undecidedResult(why string) format.Result { return format.Result{Value: "unknown", Reason: why} }

// factOf is the fact a reference names, wherever it lies.
func (x reader) factOf(ref format.Reference) *fact {
	for _, g := range x.r.facts {
		if g != nil && g.entry.Fact.Identity == ref.Fact {
			return g
		}
	}
	return nil
}

// Recognized: the referenced revision is recognized before the position; unknown while its fact
// is pending or indeterminate.
func (x reader) Recognized(ref format.Reference) format.Result {
	g := x.factOf(ref)
	member, possible := x.visible(g)
	switch {
	case member && str(g.p["content"]) == ref.Digest:
		return decided(true)
	case possible:
		return undecidedResult("the revision's fact is pending or indeterminate")
	}
	return decided(false)
}

// Current: the latest recognized revision of the referenced revision's instance; unknown while a
// later revision of it is pending or indeterminate.
func (x reader) Current(ref format.Reference) (format.Reference, format.Result) {
	g := x.factOf(ref)
	if g == nil {
		return format.Reference{}, undecidedResult("the reference resolves to no revision")
	}
	var latest *fact
	waiting := false
	for _, h := range x.r.facts {
		if h == nil || h.kind != g.kind || h.instance != g.instance {
			continue
		}
		switch member, possible := x.visible(h); {
		case member:
			latest, waiting = h, false
		case possible:
			waiting = true
		}
	}
	switch {
	case waiting:
		return format.Reference{}, undecidedResult("a later revision of the instance is pending or indeterminate")
	case latest == nil:
		return format.Reference{}, undecidedResult("no revision of the instance is recognized")
	}
	return format.Reference{Fact: latest.entry.Fact.Identity, Digest: str(latest.p["content"])}, decided(true)
}

// Authorized: the principal is the owner, or holds a grant in force that covers the use.
func (x reader) Authorized(principal, use string) format.Result {
	if principal == x.r.rootAt(x.pos).Owner {
		return decided(true)
	}
	waiting := false
	for _, g := range x.r.facts[:min(x.pos, len(x.r.facts))] {
		if g == nil || g.kind != kernelPackage+"/grant" || g.content == nil || str(g.content["principal"]) != principal {
			continue
		}
		covers := false
		for _, c := range list(g.content["covers"]) {
			covers = covers || c == use
		}
		if !covers {
			continue
		}
		member, possible := x.visible(g)
		if possible {
			waiting = true
		}
		if !member {
			continue
		}
		switch x.r.inForce(g, x.pos).value {
		case "true":
			return decided(true)
		case "unknown":
			waiting = true
		}
	}
	if waiting {
		return undecidedResult("a grant that would give the use is not decided yet")
	}
	return decided(false)
}

// environments are those of the credentials whose registration to the principal is in force at
// the position, and whether one that would add to them is not decided yet.
func (x reader) environments(principal string) (map[string]bool, bool) {
	out, waiting := map[string]bool{}, false
	for _, g := range x.r.facts[:min(x.pos, len(x.r.facts))] {
		if g == nil || g.kind != kernelPackage+"/credential-registration" || g.content == nil || str(g.content["principal"]) != principal {
			continue
		}
		member, possible := x.visible(g)
		if possible {
			waiting = true
		}
		if !member {
			continue
		}
		switch x.r.inForce(g, x.pos).value {
		case "true":
			out[str(g.content["environment"])] = true
		case "unknown":
			waiting = true
		}
	}
	return out, waiting
}

// OutsideReach: no credential registered to the principal is usable from an environment in the
// reach of any of the others.
func (x reader) OutsideReach(principal string, principals []string) format.Result {
	mine, waiting := x.environments(principal)
	for _, p := range principals {
		theirs, w := x.environments(p)
		waiting = waiting || w
		for e := range mine {
			if theirs[e] {
				return decided(false)
			}
		}
	}
	if waiting {
		return undecidedResult("a registration that would change a reach is not decided yet")
	}
	return decided(true)
}

// Holds and Permitted decide a use of the referenced revision in the reader's pass.
func (x reader) Holds(ref format.Reference, use string) format.Result {
	return x.r.nested(ref, use, x.pos, false)
}

func (x reader) Permitted(ref format.Reference, use string) format.Result {
	return x.r.nested(ref, use, x.pos, true)
}

// Findings: the findings of a check that the custody profile's evaluator signed and that are bound
// to the revision with the digest, in log order; a pending or indeterminate one is a possible
// member unless its signer is known not to be that evaluator. With candidate set, only those whose
// request names that revision as its candidate.
func (x reader) Findings(check, digest string, candidate bool) ([]format.Reference, []int) {
	evaluator := str(x.r.custodyAt(x.pos)["evaluator"])
	var refs []format.Reference
	var possible []int
	for _, g := range x.r.facts[:min(x.pos, len(x.r.facts))] {
		if g == nil || g.kind != kernelPackage+"/finding" || g.content == nil || g.content["check"] != check {
			continue
		}
		bound := false
		for _, d := range list(g.content["bound_to"]) {
			bound = bound || d == digest
		}
		member, maybe := x.visible(g)
		switch {
		case !bound || !member && !maybe:
			continue
		case member && g.signer != evaluator, maybe && g.signer != "" && g.signer != evaluator:
			continue
		}
		if candidate {
			request := x.r.requestOf(g)
			if request == nil || str(mapOf(request.content["candidate"])["digest"]) != digest {
				continue
			}
		}
		if maybe {
			possible = append(possible, len(refs))
		}
		refs = append(refs, format.Reference{Fact: g.entry.Fact.Identity, Digest: str(g.p["content"])})
	}
	return refs, possible
}

// Retained: bytes with the digest are available where state is read, carried by a fact recognized
// before the position or by the fact being decided; unknown where only a pending or indeterminate
// fact carries them.
func (x reader) Retained(digest string) format.Result {
	_, res := x.retained(digest)
	return res
}

func (x reader) retained(digest string) ([]byte, format.Result) {
	carried := func(g *fact) bool {
		for _, d := range list(g.p["captures"]) {
			if d == digest {
				return true
			}
		}
		return false
	}
	// The fact being decided carries its own bytes in every pass, whether or not the pass holds it.
	if x.self != nil && carried(x.self) {
		if b, ok := x.r.log.Blobs[digest]; ok {
			return b, decided(true)
		}
		return nil, undecidedResult("the bytes are absent")
	}
	waiting := false
	for _, g := range x.r.facts {
		if g == nil || !carried(g) {
			continue
		}
		member, possible := x.visible(g)
		switch {
		case member:
			b, ok := x.r.log.Blobs[digest]
			if !ok {
				return nil, undecidedResult("the bytes are absent")
			}
			return b, decided(true)
		case possible:
			waiting = true
		}
	}
	if waiting {
		return nil, undecidedResult("only a pending or indeterminate fact carries the bytes")
	}
	return nil, decided(false)
}

// quotedLimit is how many bytes of a citation quoted reads.
const quotedLimit = 65536

// Quoted: the quote occurs, byte for byte, within the lines the location names of the retained
// bytes with the digest.
func (x reader) Quoted(digest, location, quote string) format.Result {
	b, res := x.retained(digest)
	if res.Value != "true" {
		return res
	}
	first, last, ok := lineRange(location)
	// Lines are split at line feeds, without their terminators, so a final line feed ends the last
	// line and starts none.
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if !ok || first < 1 || last > len(lines) {
		return decided(false)
	}
	text := strings.Join(lines[first-1:last], "\n")
	if len(text) > quotedLimit {
		return undecidedResult("the citation is longer than a verifier reads")
	}
	return decided(strings.Contains(text, quote))
}

// location is a line, L52, or a range of lines, L52-L60, numbered from 1.
var location = regexp.MustCompile(`^L([1-9][0-9]*)(?:-L([1-9][0-9]*))?$`)

// lineRange reads a location as its first and last line.
func lineRange(s string) (int, int, bool) {
	m := location.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	first, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, 0, false
	}
	last := first
	if m[2] != "" {
		if last, err = strconv.Atoi(m[2]); err != nil || last < first {
			return 0, 0, false
		}
	}
	return first, last, true
}

// nested decides a use of a referenced revision at a position in this pass: over what the pass
// recognizes, and again in the use's own second pass, which keeps what this pass set aside, set
// aside. It holds only if it holds in both, is false if it fails in either, and is otherwise
// unknown; exceptions in force count where they are asked for. A read that comes back to a use
// while it is being decided, for the same revision at the same position in the same pass, is
// unknown.
func (r *run) nested(ref format.Reference, use string, at int, exceptions bool) format.Result {
	subj := r.factByIdentity(ref.Fact, at)
	if subj == nil || subj.status != Recognized || str(subj.p["content"]) != ref.Digest {
		return undecidedResult("the reference resolves to no revision that is recognized")
	}
	node := mapOf(mapOf(r.definition(subj)["uses"])[use])
	if node == nil {
		return undecidedResult("the revision's definition declares no use " + use)
	}
	key := fmt.Sprintf("%s|%s|%d|%s", use, ref.Fact, at, r.passKey())
	if r.visiting[key] {
		return undecidedResult("the read is cyclic")
	}
	r.visiting[key] = true
	defer delete(r.visiting, key)
	full := subj.kind + ".uses." + use
	requires := requirements(subj.kind, r.definition(subj), node)
	var covering []int64
	pass := func(p *run) string {
		value := "true"
		for name, o := range p.passConditions(subj, subj.pkg, subj.def, use, node, at, ref.Digest) {
			if o.value == "true" {
				continue
			}
			if waivable, _ := mapOf(requires[name])["waivable"].(bool); exceptions && waivable {
				if e, ok := p.coveringException(full, ref.Digest, name, at); ok {
					covering = append(covering, int64(e))
					continue
				}
			}
			if o.value == "false" {
				return "false"
			}
			value = "unknown"
		}
		return value
	}
	in := r.useInputs(subj, use, at)
	beneficiaries, known, err := r.universeFor(subj).EvaluateUseList(subj.pkg, subj.def, use, "beneficiaries", in, reader{r: r, pos: at})
	if err != nil {
		return undecidedResult("the use's beneficiaries cannot be read")
	}
	first := pass(r)
	if known.Value != "true" {
		// Undecided beneficiaries leave the second pass unknown, and a first pass that fails still
		// decides the use.
		if first == "false" {
			return decided(false)
		}
		return undecidedResult("the use's beneficiaries are not decided")
	}
	sort.Strings(beneficiaries)
	// The facts the second pass decides again are no reads of the use, so what they cover is not
	// gathered.
	collect := r.collect
	r.collect = nil
	second, _ := r.secondPass(dedupe(beneficiaries), subj, at)
	r.collect, second.collect = collect, collect
	switch again := pass(second); {
	case first == "false" || again == "false":
		return decided(false)
	case first == "true" && again == "true":
		if exceptions && collect != nil {
			*collect = append(*collect, covering...)
		}
		return decided(true)
	}
	return undecidedResult("a condition of the use is not decided")
}

// passKey names the pass by the facts it sets aside and keeps.
func (r *run) passKey() string {
	var aside, kept []int
	for i := range r.aside {
		aside = append(aside, i)
	}
	for i := range r.kept {
		kept = append(kept, i)
	}
	sort.Ints(aside)
	sort.Ints(kept)
	return fmt.Sprint(aside, kept)
}
