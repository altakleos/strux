package verify

import (
	"sort"
	"strings"
	"time"

	"github.com/altakleos/strux/format"
)

// Status is a fact's recognition status.
type Status int

// The statuses an entry can have.
const (
	Recognized Status = iota + 1
	NotRecognized
	Pending
	Indeterminate
	Invalid
)

// fact is an entry that holds a fact, with what the steps found of it.
type fact struct {
	pos          int
	entry        Entry
	p            map[string]any // the signed object
	kind         string         // package/definition
	pkg, def     string
	content      map[string]any // nil where the content is absent or does not read
	bootstrap    bool
	trustRootAct bool
	signer       string // "" while unknown
	instance     string
	status       Status
	failed       []string
	unknown      []string
	limits       []string
	conflict     *Conflict
}

// Conflict is the earlier recognized fact that holds what a later one needed.
type Conflict struct {
	With int64 `json:"with"`
	Copy bool  `json:"copy"`
}

// outcome is a condition's result: satisfied, unsatisfied, or unknown, with the limit an unknown
// met, if any.
type outcome struct {
	value string // "true", "false" or "unknown"
	limit string
}

var (
	holds = outcome{value: "true"}
	fails = outcome{value: "false"}
)

func waits() outcome           { return outcome{value: "unknown"} }
func limited(l string) outcome { return outcome{value: "unknown", limit: l} }

// The conditions of recognize-fact, by name, in the form a result names them.
func recognizeCondition(name string) string {
	return kernelPackage + "/verification.uses.recognize-fact.requires." + name
}

// decideFacts decides every entry read, in log order: the bootstrap facts first, each at its own
// position, then every other fact by the conditions of recognize-fact, over what is recognized
// before it. The chain acts were judged first and keep what that step found.
func (r *run) decideFacts(universe *format.Universe, packages map[string]string) {
	r.universe, r.packageOf = universe, packages
	read := r.judgeRead()
	r.readResult = read
	end := int64(r.n) - 1
	if read.EndsAfter != nil {
		end = *read.EndsAfter
	}
	recognizedActs := map[int]bool{}
	for _, p := range r.chainActs {
		recognizedActs[p] = true
	}
	r.facts = make([]*fact, len(r.entries))
	for i, e := range r.entries {
		if e.Fact == nil {
			continue
		}
		f := r.newFact(i, e)
		r.facts[i] = f
		switch {
		case int64(i) > end:
			f.status, f.limits = Indeterminate, []string{"read-behind"}
		case f.isChainAct():
			r.decideChainAct(f, recognizedActs[i])
		default:
			r.decide(f)
		}
	}
}

func (r *run) newFact(i int, e Entry) *fact {
	f := &fact{pos: i, entry: e, p: e.Fact.Fact, bootstrap: i < len(r.root.Bootstrap)}
	f.kind = str(f.p["kind"])
	f.pkg, f.def, _ = strings.Cut(f.kind, "/")
	if c, ok := r.content(e).(map[string]any); ok {
		f.content = c
	}
	f.trustRootAct = f.bootstrap || f.isChainAct() || f.kind == kernelPackage+"/credential-registration" && f.content != nil && f.content["principal"] == r.root.Owner && f.p["key"] == nil
	if f.trustRootAct {
		f.signer = r.root.Owner
	}
	f.instance = e.Fact.Identity
	if expected := str(f.p["expected"]); expected != "" {
		if pred := r.factByIdentity(expected, i); pred != nil {
			f.instance = pred.instance
		}
	}
	return f
}

func (f *fact) isChainAct() bool {
	return f.kind == kernelPackage+"/compromise-revocation" || f.kind == kernelPackage+"/trust-root"
}

// decideChainAct gives a chain act the status the first step found, naming what it failed.
func (r *run) decideChainAct(f *fact, recognized bool) {
	if recognized {
		f.status = Recognized
		return
	}
	f.status = NotRecognized
	if f.p["package"] != r.kernel.digest {
		f.failed = append(f.failed, recognizeCondition("package-active"))
	}
	if f.p["key"] != nil || !r.signedByTrustRoot(f.entry) {
		f.failed = append(f.failed, recognizeCondition("signature"))
	}
	if r.structureOf(f).value != "true" {
		f.failed = append(f.failed, recognizeCondition("structure"))
	}
	if len(f.failed) == 0 {
		f.failed = append(f.failed, recognizeCondition("order"))
	}
	sort.Strings(f.failed)
}

// decide decides a fact by the conditions of recognize-fact, over what is recognized before its
// position, and gives it its status.
func (r *run) decide(f *fact) {
	conditions := map[string]outcome{
		"package-active": r.packageActive(f),
		"signature":      r.signature(f),
		"structure":      r.structureOf(f),
	}
	if !f.trustRootAct {
		conditions["not-compromised"] = r.notCompromised(f)
		conditions["not-in-broken-part"] = r.notInBrokenPart(f)
		conditions["authority"] = r.authority(f)
		conditions["supply-chain"] = r.supplyChain(f)
	}
	if !f.bootstrap {
		conditions["in-time"] = r.inTime(f)
	}
	use, decidingUse := r.decidingUse(f)
	conditions["deciding-use"] = decidingUse
	conditions["order"] = r.order(f)
	var useFailed, useUnknown []string
	if conditions["structure"].value == "true" && decidingUse.value == "true" {
		var o outcome
		o, useFailed, useUnknown = r.useConditions(f, use)
		conditions["conditions"] = o
	}
	var limits []string
	for _, name := range sortedKeys(conditions) {
		switch o := conditions[name]; o.value {
		case "false":
			f.failed = append(f.failed, recognizeCondition(name))
		case "unknown":
			f.unknown = append(f.unknown, recognizeCondition(name))
			if o.limit != "" {
				limits = append(limits, o.limit)
			}
		}
	}
	f.failed = append(f.failed, useFailed...)
	f.unknown = append(f.unknown, useUnknown...)
	sort.Strings(f.failed)
	sort.Strings(f.unknown)
	_, upperClosed := r.upperBound(int64(f.pos))
	switch {
	case len(f.failed) > 0:
		f.status = NotRecognized
	case len(f.unknown) == 0:
		f.status = Recognized
		f.unknown = nil
	case !upperClosed && len(limits) == 0:
		f.status = Pending
	case len(limits) > 0 && (!upperClosed || len(limits) == len(f.unknown)):
		f.status, f.limits = Indeterminate, orderedLimits(limits)
	default:
		f.status = NotRecognized
	}
}

// limitKinds lists the kinds of limit in the order a result gives them.
var limitKinds = []string{"read-behind", "chain-act", "chain-short", "bootstrap", "absent-bytes", "interpretation", "resources", "indeterminate-fact"}

func orderedLimits(ls []string) []string {
	var out []string
	for _, k := range limitKinds {
		for _, l := range ls {
			if l == k {
				out = append(out, k)
				break
			}
		}
	}
	return out
}

// packageActive: the configuration in force at the fact's position activates the package
// revision it names; for a bootstrap fact, the bootstrap configuration does.
func (r *run) packageActive(f *fact) outcome {
	for digest := range r.packageOf {
		if f.p["package"] == digest {
			return holds
		}
	}
	return fails
}

// signature: a trust-root act names no key and has the trust root's threshold of its keys; any
// other fact names a key whose registration is in force at its position, may sign its kind, and
// is no key of the trust root.
func (r *run) signature(f *fact) outcome {
	key := str(f.p["key"])
	if f.trustRootAct {
		if key == "" && r.signedByTrustRoot(f.entry) {
			return holds
		}
		return fails
	}
	if key == "" || r.root.isKey(key) {
		return fails
	}
	registration, o := r.registrationOf(key, f.pos)
	if o.value != "true" {
		return o
	}
	f.signer = str(registration.content["principal"])
	for _, k := range list(registration.content["may_sign"]) {
		if k == f.kind {
			return holds
		}
	}
	return fails
}

// registrationOf finds the registration in force at a position for a credential's key.
func (r *run) registrationOf(key string, pos int) (*fact, outcome) {
	var found *fact
	result := fails
	for _, g := range r.facts[:pos] {
		if g == nil || g.kind != kernelPackage+"/credential-registration" || g.content == nil {
			continue
		}
		credential, _ := g.content["credential"].(map[string]any)
		if credential["public_key"] != key {
			continue
		}
		switch o := r.inForce(g, pos); o.value {
		case "true":
			found, result = g, holds
		case "unknown":
			if result.value != "true" {
				result = o
			}
		}
	}
	return found, result
}

// inForce: a decision is in force at a position when its fact was recognized before it, it is the
// current revision of its instance there, and the position's time lies within its validity.
func (r *run) inForce(g *fact, pos int) outcome {
	switch g.status {
	case Recognized:
	case Pending:
		return waits()
	case Indeterminate:
		return limited("indeterminate-fact")
	default:
		return fails
	}
	for _, h := range r.facts[g.pos+1 : pos] {
		if h != nil && h.p["expected"] == g.entry.Fact.Identity {
			switch h.status {
			case Recognized:
				return fails
			case Pending:
				return waits()
			case Indeterminate:
				return limited("indeterminate-fact")
			}
		}
	}
	return r.withinValidity(g.content, int64(pos))
}

// withinValidity tests a position's time against a validity: its start by the lower bound alone,
// which fails where there is none, and its end by the upper bound, which waits while it is open.
func (r *run) withinValidity(content map[string]any, pos int64) outcome {
	validity, _ := content["validity"].(map[string]any)
	if validity == nil {
		return holds
	}
	if s := str(validity["not_before"]); s != "" {
		start, err := format.ParseTime(s)
		lower, ok := r.lowerBound(pos)
		if err != nil || !ok || lower.Before(start) {
			return fails
		}
	}
	if s := str(validity["not_after"]); s != "" {
		end, err := format.ParseTime(s)
		if err != nil {
			return fails
		}
		upper, ok := r.upperBound(pos)
		switch {
		case !ok:
			return waits()
		case !upper.Before(end):
			return fails
		}
	}
	return holds
}

func (r *run) lowerBound(pos int64) (time.Time, bool) {
	lower, _, ok, _ := r.bounds(pos)
	return lower, ok
}

func (r *run) upperBound(pos int64) (time.Time, bool) {
	_, upper, _, ok := r.bounds(pos)
	return upper, ok
}

// notCompromised: no recognized compromise revocation covers the fact's key at its position.
func (r *run) notCompromised(f *fact) outcome {
	if from, ok := r.compromised[str(f.p["key"])]; ok && int64(f.pos) >= from {
		return fails
	}
	return holds
}

// notInBrokenPart: the fact does not lie after the start of a break.
func (r *run) notInBrokenPart(f *fact) outcome {
	if r.readResult.BreakAfter != nil && int64(f.pos) > *r.readResult.BreakAfter {
		return fails
	}
	return holds
}

// inTime: the position's time lies before the fact's append deadline, and a fact of a governing
// definition carries one; the validity of the authority it relies on is tested where the
// authority is read.
func (r *run) inTime(f *fact) outcome {
	deadline := str(f.p["append_by"])
	if deadline == "" {
		if r.governing(f) && !f.trustRootAct {
			return fails
		}
		return holds
	}
	by, err := format.ParseTime(deadline)
	if err != nil {
		return fails
	}
	upper, ok := r.upperBound(int64(f.pos))
	switch {
	case !ok:
		return waits()
	case upper.Before(by):
		return holds
	}
	return fails
}

func (r *run) governing(f *fact) bool {
	d := r.definition(f)
	governing, _ := d["governing"].(bool)
	return governing
}

func (r *run) definition(f *fact) map[string]any {
	p := r.universe.Packages[f.pkg]
	if p == nil || p.Defs[f.def] == nil {
		return nil
	}
	return p.Defs[f.def].Raw
}

// authority: a trust-root act, a fact the owner signed, or a revocation signed by whoever signed
// what it ends needs no grant. Any other fact names in under a grant its signer holds, in force at
// its position, that covers the use deciding it.
func (r *run) authority(f *fact) outcome {
	if f.signer == r.root.Owner && f.signer != "" {
		return holds
	}
	under, _ := f.p["under"].(map[string]any)
	if under == nil {
		return fails
	}
	grant := r.factByIdentity(str(under["fact"]), f.pos)
	if grant == nil || grant.kind != kernelPackage+"/grant" || grant.content == nil {
		return fails
	}
	if o := r.inForce(grant, f.pos); o.value != "true" {
		return o
	}
	if f.signer == "" {
		return waits()
	}
	if grant.content["principal"] != f.signer {
		return fails
	}
	use, ok := r.decidingUse(f)
	if ok.value != "true" {
		return fails
	}
	covered := f.kind + ".uses." + use
	if f.kind == kernelPackage+"/exception" && f.content != nil {
		covered = str(f.content["use"]) + ".except"
	}
	for _, c := range list(grant.content["covers"]) {
		if c == covered {
			if len(list(grant.content["conditions"])) > 0 {
				return limited("interpretation")
			}
			return holds
		}
	}
	return fails
}

// supplyChain: a component the custody profile names records the configurations it ran, each one
// the custody profile pins for it.
func (r *run) supplyChain(f *fact) outcome {
	custody := r.custodyAt(f.pos)
	if custody == nil {
		return holds
	}
	pinned, _ := custody["configuration"].(map[string]any)
	for _, role := range []string{"verifier", "evaluator"} {
		if f.signer == "" || custody[role] != f.signer {
			continue
		}
		recorded := list(f.p["configuration"])
		if len(recorded) == 0 {
			return fails
		}
		allowed := map[any]bool{}
		for _, d := range list(pinned[role]) {
			allowed[d] = true
		}
		for _, d := range recorded {
			if !allowed[d] {
				return fails
			}
		}
	}
	return holds
}

// decidingUse is the use that decides a fact: the one its operation of its own kind names, or its
// kind's recognize use; a fact that names an expected predecessor needs a use that decides
// revisions.
func (r *run) decidingUse(f *fact) (string, outcome) {
	d := r.definition(f)
	if d == nil {
		return "", fails
	}
	uses, _ := d["uses"].(map[string]any)
	use := ""
	if op := str(f.p["operation"]); strings.HasPrefix(op, f.kind+".operations.") {
		operations, _ := d["operations"].(map[string]any)
		node, _ := operations[strings.TrimPrefix(op, f.kind+".operations.")].(map[string]any)
		use = str(node["use"])
	} else if uses["recognize"] != nil {
		use = "recognize"
	}
	node, _ := uses[use].(map[string]any)
	if node == nil {
		return "", fails
	}
	if revises, _ := node["revises"].(bool); str(f.p["expected"]) != "" && !revises {
		return use, fails
	}
	return use, holds
}

// order: the fact's predecessor is a recognized fact of its kind that no earlier fact took, it
// starts no second instance of a singleton definition, repeats no operation identity of its
// signer, and is no copy.
func (r *run) order(f *fact) outcome {
	id := f.entry.Fact.Identity
	expected := str(f.p["expected"])
	singleton, _ := r.definition(f)["singleton"].(bool)
	result := holds
	for _, g := range r.facts[:f.pos] {
		if g == nil {
			continue
		}
		taken := g.status == Recognized
		switch {
		case g.entry.Fact.Identity == id && taken:
			f.conflict = &Conflict{With: int64(g.pos), Copy: true}
			return fails
		case expected != "" && str(g.p["expected"]) == expected && g.kind == f.kind && taken:
			f.conflict = &Conflict{With: int64(g.pos)}
			return fails
		case singleton && expected == "" && g.kind == f.kind && str(g.p["expected"]) == "" && taken:
			f.conflict = &Conflict{With: int64(g.pos)}
			return fails
		case str(f.p["operation_id"]) != "" && g.p["operation_id"] == f.p["operation_id"] && g.p["operation"] == f.p["operation"] && g.signer == f.signer && taken:
			f.conflict = &Conflict{With: int64(g.pos)}
			return fails
		}
	}
	if expected != "" {
		pred := r.factByIdentity(expected, f.pos)
		switch {
		case pred == nil || pred.kind != f.kind:
			return fails
		case pred.status == Pending:
			result = waits()
		case pred.status == Indeterminate:
			result = limited("indeterminate-fact")
		case pred.status != Recognized:
			return fails
		}
	}
	return result
}

// structureOf: the content conforms to the structure of its kind.
func (r *run) structureOf(f *fact) outcome {
	if f.entry.Fact.ContentAbsent {
		return limited("absent-bytes")
	}
	p := r.universe.Packages[f.pkg]
	if p == nil || p.Defs[f.def] == nil || !p.Defs[f.def].HasStructure() || f.content == nil {
		return fails
	}
	if len(format.Conforms(p.Defs[f.def].Decl, f.content)) > 0 {
		return fails
	}
	return holds
}

// factByIdentity is the fact before a position whose identity is id.
func (r *run) factByIdentity(id string, before int) *fact {
	for _, g := range r.facts[:min(before, len(r.facts))] {
		if g != nil && g.entry.Fact.Identity == id {
			return g
		}
	}
	return nil
}

// custodyAt is the content of the custody profile in force at a position.
func (r *run) custodyAt(pos int) map[string]any {
	var current map[string]any
	for _, g := range r.facts[:min(pos, len(r.facts))] {
		if g != nil && g.kind == kernelPackage+"/custody-profile" && g.status == Recognized {
			current = g.content
		}
	}
	if current == nil && r.bootstrapCustody != nil {
		return r.bootstrapCustody
	}
	return current
}
