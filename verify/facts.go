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
	signer       string // "" where it is unknown or absent
	// signerUnknown is set where the registration that names the signer is undecided, as against
	// absent, where no registration of the credential is in force.
	signerUnknown bool
	instance      string
	status        Status
	failed        []string
	unknown       []string
	limits        []string
	conflict      *Conflict
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
func (r *run) decideFacts(universe *format.Universe) {
	r.universe, r.universes = universe, []*format.Universe{universe}
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
		if f.status == Recognized && !f.bootstrap && f.kind == kernelPackage+"/configuration" {
			r.activate(f)
		}
	}
}

// bootstrapFirst is the condition of start that a fact lists where the log's bootstrap fails.
var bootstrapFirst = kernelPackage + "/verification.uses.start.requires.bootstrap-first"

// bootstrapInPlace says whether the log's first entries are the bootstrap facts the initial trust
// root lists, in its order.
func (r *run) bootstrapInPlace() bool {
	if len(r.entries) < len(r.root.Bootstrap) {
		return false
	}
	for i, id := range r.root.Bootstrap {
		if e := r.entries[i]; e.Fact == nil || e.Fact.Identity != id {
			return false
		}
	}
	return true
}

// failBootstrap decides a log whose first entries are not its bootstrap: nothing in it is
// recognized, and every fact lists the start condition alone. A fact past the end of the read is
// indeterminate as on any read.
func (r *run) failBootstrap() {
	read := r.judgeRead()
	r.readResult = read
	end := int64(r.n) - 1
	if read.EndsAfter != nil {
		end = *read.EndsAfter
	}
	r.facts = make([]*fact, len(r.entries))
	for i, e := range r.entries {
		if e.Fact == nil {
			continue
		}
		f := &fact{pos: i, entry: e, p: e.Fact.Fact}
		r.facts[i] = f
		if int64(i) > end {
			f.status, f.limits = Indeterminate, []string{"read-behind"}
			continue
		}
		f.status, f.failed = NotRecognized, []string{bootstrapFirst}
	}
}

// applyBootstrap decides what a bootstrap fact that is not recognized leaves of the rest: that fact
// lists what it fails, and every other fact lists the start condition alone. A bootstrap fact that
// is pending or indeterminate is in no vector yet, and this verifier leaves the rest as decided.
func (r *run) applyBootstrap() {
	failed := false
	for _, f := range r.facts[:len(r.root.Bootstrap)] {
		failed = failed || f.status == NotRecognized
	}
	if !failed {
		return
	}
	for _, f := range r.facts {
		if f == nil || f.bootstrap && f.status == NotRecognized || f.status == Indeterminate && len(f.limits) == 1 && f.limits[0] == "read-behind" {
			continue
		}
		f.status, f.failed, f.unknown, f.limits, f.conflict = NotRecognized, []string{bootstrapFirst}, nil, nil, nil
	}
}

// activate gives a recognized configuration the universe of the package revisions it pins, in
// which every fact that names one of them is decided, so a later configuration changes nothing of
// how an earlier fact is judged. The configuration was recognized only if they conform together.
func (r *run) activate(config *fact) {
	pkgs := map[string]*format.Package{}
	for _, item := range list(config.content["packages"]) {
		g := r.factByIdentity(str(mapOf(item)["fact"]), config.pos)
		if g == nil || g.content == nil {
			return
		}
		parsed, err := format.Parse(r.log.Blobs[str(g.content["bytes"])])
		if err != nil {
			return
		}
		name := str(g.content["name"])
		pkgs[name] = format.Build(name, name, parsed)
	}
	if u, problems := format.Prepare(pkgs, format.Kernel(kernelPackage)); len(problems) == 0 {
		r.universe, r.universes = u, append(r.universes, u)
	}
}

// universeFor is the universe a fact is decided in: the latest that holds the package revision
// it names, and otherwise the latest of all.
func (r *run) universeFor(f *fact) *format.Universe {
	for i := len(r.universes) - 1; i >= 0; i-- {
		if p := r.universes[i].Packages[f.pkg]; p != nil && p.Digest == str(f.p["package"]) {
			return r.universes[i]
		}
	}
	return r.universe
}

// configurationAt is the content of the configuration in force at a position: the bootstrap's for
// a bootstrap fact, and otherwise its latest recognized revision before the position, with the
// revisions after that one that are still pending or indeterminate.
func (r *run) configurationAt(pos int, bootstrap bool) (map[string]any, []*fact) {
	var current map[string]any
	for _, e := range r.entries[:min(len(r.entries), len(r.root.Bootstrap))] {
		if e.Fact != nil && e.Fact.Fact["kind"] == kernelPackage+"/configuration" {
			current, _ = r.content(e).(map[string]any)
		}
	}
	if bootstrap {
		return current, nil
	}
	var waiting []*fact
	for _, g := range r.facts[:min(pos, len(r.facts))] {
		if g == nil || g.bootstrap || g.kind != kernelPackage+"/configuration" {
			continue
		}
		switch g.status {
		case Recognized:
			current, waiting = g.content, nil
		case Pending, Indeterminate:
			waiting = append(waiting, g)
		}
	}
	return current, waiting
}

func (r *run) newFact(i int, e Entry) *fact {
	f := &fact{pos: i, entry: e, p: e.Fact.Fact, bootstrap: i < len(r.root.Bootstrap)}
	f.kind = str(f.p["kind"])
	f.pkg, f.def, _ = strings.Cut(f.kind, "/")
	if c, ok := r.content(e).(map[string]any); ok {
		f.content = c
	}
	f.trustRootAct = f.bootstrap || f.isChainAct() || f.kind == kernelPackage+"/credential-registration" && f.content != nil && f.content["principal"] == r.rootAt(i).Owner && f.p["key"] == nil
	if f.trustRootAct {
		f.signer = r.rootAt(i).Owner
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
	if failed, judged := r.chainFailed[f.pos]; judged {
		f.failed = failed
		return
	}
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
	// Under a package revision the configuration does not activate, nothing the package defines is
	// decided: neither the structure of its kind nor its deciding use.
	active := r.packageActive(f)
	conditions := map[string]outcome{
		"package-active": active,
		"signature":      r.signature(f),
	}
	if active.value == "true" {
		conditions["structure"] = r.structureOf(f)
	}
	if !f.trustRootAct {
		conditions["not-compromised"] = r.notCompromised(f)
		conditions["not-in-broken-part"] = r.notInBrokenPart(f)
		// Under an inactive package, the use an operation of the fact's own kind names is the
		// package's to say, so the authority it needs is not decided.
		if active.value == "true" || !strings.HasPrefix(str(f.p["operation"]), f.kind+".operations.") {
			conditions["authority"] = r.authority(f)
		}
		conditions["supply-chain"] = r.supplyChain(f)
	}
	if !f.bootstrap {
		conditions["in-time"] = r.inTime(f)
	}
	use, decidingUse := r.decidingUse(f)
	if active.value == "true" {
		conditions["deciding-use"] = decidingUse
	}
	conditions["order"] = r.order(f)
	var useFailed, useUnknown []string
	if active.value == "true" && conditions["structure"].value == "true" && decidingUse.value == "true" {
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
	config, waiting := r.configurationAt(f.pos, f.bootstrap)
	active := r.pins(config, str(f.p["package"]))
	// A later revision that is still undecided leaves the answer open where it would change it.
	for _, g := range waiting {
		if r.pins(g.content, str(f.p["package"])) != active {
			if g.status == Indeterminate {
				return limited("indeterminate-fact")
			}
			return waits()
		}
	}
	if active {
		return holds
	}
	return fails
}

// pins reports whether a configuration pins the package revision with a digest.
func (r *run) pins(config map[string]any, digest string) bool {
	for _, item := range list(config["packages"]) {
		for _, e := range r.entries {
			if e.Fact == nil || e.Fact.Identity != str(mapOf(item)["fact"]) {
				continue
			}
			if c, _ := r.content(e).(map[string]any); c != nil && c["digest"] == digest {
				return true
			}
		}
	}
	return false
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
	if key == "" || r.rootAt(f.pos).isKey(key) {
		return fails
	}
	registration, o := r.registrationOf(key, f.pos)
	if o.value != "true" {
		// The signer is derived from the registration in force for the credential: unknown while
		// that is undecided, and absent where no registration is in force.
		f.signerUnknown = o.value == "unknown"
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
// current revision of its instance there, no recognized revocation of it precedes the position,
// the position's time lies within its validity, and the authority it was made under is in force
// there.
func (r *run) inForce(g *fact, pos int) outcome {
	if g.pos >= pos {
		return fails // a decision is recorded before the position where it is in force
	}
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
		if h != nil && (h.p["expected"] == g.entry.Fact.Identity || h.revokes(g)) {
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
	if o := r.withinValidity(g.content, int64(pos)); o.value != "true" {
		return o
	}
	if under, _ := g.p["under"].(map[string]any); under != nil {
		issuer := r.factByIdentity(str(under["fact"]), g.pos)
		if issuer == nil {
			return fails
		}
		return r.inForce(issuer, pos)
	}
	return holds
}

// revokes says whether the fact is a revocation of the decision g recorded.
func (h *fact) revokes(g *fact) bool {
	if h.kind != kernelPackage+"/revocation" || h.content == nil {
		return false
	}
	target, _ := h.content["revokes"].(map[string]any)
	return target != nil && target["fact"] == g.entry.Fact.Identity
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
	p := r.universeFor(f).Packages[f.pkg]
	if p == nil || p.Defs[f.def] == nil {
		return nil
	}
	return p.Defs[f.def].Raw
}

// authority: a trust-root act, a fact the owner signed, or a revocation signed by whoever signed
// what it ends needs no grant. Any other fact names in under a grant its signer holds, in force at
// its position, that covers the use deciding it.
func (r *run) authority(f *fact) outcome {
	if f.signer == r.rootAt(f.pos).Owner && f.signer != "" {
		return holds
	}
	if f.kind == kernelPackage+"/revocation" && f.content != nil {
		target, _ := f.content["revokes"].(map[string]any)
		if ended := r.factByIdentity(str(target["fact"]), f.pos); ended != nil && f.signer != "" && ended.signer == f.signer {
			return holds
		}
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
	switch {
	case f.signer == "" && f.signerUnknown:
		return waits()
	case f.signer == "" || grant.content["principal"] != f.signer:
		return fails
	}
	// What the grant must cover: the use deciding the fact, for an exception the use it names with
	// .except, and for a grant every use it covers.
	var needed []string
	switch {
	case f.kind == kernelPackage+"/exception" && f.content != nil:
		needed = []string{str(f.content["use"]) + ".except"}
	case f.kind == kernelPackage+"/grant" && f.content != nil:
		for _, c := range list(f.content["covers"]) {
			needed = append(needed, str(c))
		}
	default:
		use, ok := r.decidingUse(f)
		switch {
		case r.packageActive(f).value != "true" && str(f.p["operation"]) == "":
			// Nothing the inactive package defines is decided, and the use its kind names is read.
			use = "recognize"
		case ok.value != "true":
			return fails
		}
		needed = []string{f.kind + ".uses." + use}
	}
	covers := map[string]bool{}
	for _, c := range list(grant.content["covers"]) {
		covers[str(c)] = true
	}
	for _, n := range needed {
		if !covers[n] {
			return fails
		}
	}
	if len(list(grant.content["conditions"])) > 0 {
		return limited("interpretation")
	}
	return holds
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
	p := r.universeFor(f).Packages[f.pkg]
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
