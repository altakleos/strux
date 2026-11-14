package verify

import (
	"errors"
	"sort"
	"time"

	"github.com/altakleos/strux/format"
)

// run is one verification: the log, the trust root, how many entries were read, and what the
// steps have found so far.
type run struct {
	log     *Log
	root    *TrustRoot
	n       int
	ended   bool
	entries []Entry
	kernel  *kernel
	// compromised holds, for each credential a recognized compromise revocation names, the
	// earliest position from which one states it compromised.
	compromised map[string]int64
	chainActs   []int
	anchors     []*anchor
	// What deciding the facts needs and finds: the packages the bootstrap configuration pins, by
	// the digest of their definitions, the universe they are checked in, the bootstrap's custody
	// profile, the read, and every fact read.
	universe         *format.Universe
	packageOf        map[string]string
	bootstrapCustody map[string]any
	readResult       ReadResult
	facts            []*fact
}

// anchor is an anchor that was read, with what the second step made of it.
type anchor struct {
	identity   string
	witness    string
	key        string
	log        string
	position   int64
	head       string
	observed   time.Time
	previous   string
	recognized bool
	extended   bool // the log holds its position and has its head there
}

// AnchorsResult is what a run made of the anchors it read.
type AnchorsResult struct {
	Recognized    []string       `json:"recognized"`
	NotRecognized []string       `json:"not_recognized"`
	NotExtended   []string       `json:"not_extended"`
	Unreadable    int            `json:"unreadable"`
	Equivocations []Equivocation `json:"equivocations"`
}

// Equivocation is anchors of one witness that name the same previous anchor.
type Equivocation struct {
	Witness  string   `json:"witness"`
	Previous string   `json:"previous,omitempty"`
	Anchors  []string `json:"anchors"`
}

// ReadResult is what a run found of the log against the recognized anchors.
type ReadResult struct {
	Behind      bool    `json:"behind"`
	EndsAfter   *int64  `json:"ends_after,omitempty"`
	BreakAfter  *int64  `json:"break_after,omitempty"`
	Disagreeing []int64 `json:"disagreeing"`
}

func newRun(l *Log, root *TrustRoot, n int, ended bool) (*run, error) {
	if n > len(l.Records) {
		return nil, errors.New("the read counts more entries than the log holds")
	}
	r := &run{log: l, root: root, n: n, ended: ended, compromised: map[string]int64{}}
	r.entries = l.ReadEntries(root.Log, n)
	k, err := r.bootstrapKernel()
	if err != nil {
		return nil, err
	}
	r.kernel = k
	return r, nil
}

// judgeChainActs judges the chain acts in log order, before anything else. Under the initial trust
// root, the one chain act of the vectors is a compromise revocation: a trust-root act, signed by as
// many distinct keys of the trust root as its threshold names, decided by the kernel the bootstrap
// configuration pins, with content of its kind's structure, and neither a copy of an earlier chain
// act nor a repeat of the operation identity of one.
func (r *run) judgeChainActs() error {
	decl := r.kernel.pkg.Defs["compromise-revocation"]
	if decl == nil {
		return errors.New("the kernel defines no compromise revocation")
	}
	seen := map[string]bool{}
	operations := map[[2]string]bool{}
	for _, e := range r.entries {
		if e.Fact == nil {
			continue
		}
		p := e.Fact.Fact
		switch p["kind"] {
		case kernelPackage + "/trust-root":
			return errors.New("a trust-root revision is not verified yet")
		case kernelPackage + "/compromise-revocation":
		default:
			continue
		}
		if p["package"] != r.kernel.digest || p["key"] != nil || !r.signedByTrustRoot(e) {
			continue
		}
		content, ok := r.content(e).(map[string]any)
		if !ok || len(format.Conforms(decl.Decl, content)) > 0 {
			continue
		}
		op := [2]string{str(p["operation"]), str(p["operation_id"])}
		if seen[e.Fact.Identity] || op[1] != "" && operations[op] {
			continue
		}
		seen[e.Fact.Identity] = true
		if op[1] != "" {
			operations[op] = true
		}
		r.chainActs = append(r.chainActs, e.Position)
		credential, _ := content["credential"].(string)
		from, _ := content["from"].(int64)
		if earlier, ok := r.compromised[credential]; !ok || from < earlier {
			r.compromised[credential] = from
		}
	}
	return nil
}

// signedByTrustRoot reports whether as many distinct keys of the trust root as its threshold names
// signed the entry.
func (r *run) signedByTrustRoot(e Entry) bool {
	distinct := map[string]bool{}
	for _, k := range e.Signers {
		if r.root.isKey(k) {
			distinct[k] = true
		}
	}
	return len(distinct) >= r.root.Threshold
}

// judgeAnchors reads the anchors and recognizes each witness's along the chain their previous
// members make, from the one that names none. An anchor counts only for a witness the trust root
// names, for this log, and where no compromise revocation covers its key at its position. Two that
// name the same previous anchor are an equivocation, and neither, nor any later one of that
// witness, is recognized. Each must lie no earlier, in position or observed time, than the one it
// names, and where the log extends it, the log must extend that one too.
func (r *run) judgeAnchors(read [][]byte) AnchorsResult {
	var res AnchorsResult
	byWitness := map[string][]*anchor{}
	for _, b := range read {
		identity, a, err := format.ReadAnchor(b)
		if err != nil {
			res.Unreadable++
			continue
		}
		position, _ := a["position"].(int64)
		observed, err := format.ParseTime(str(a["observed"]))
		if err != nil {
			res.Unreadable++
			continue
		}
		an := &anchor{identity: identity, key: str(a["key"]), log: str(a["log"]), position: position, head: str(a["head"]), observed: observed, previous: str(a["previous"])}
		if position < int64(r.n) {
			an.extended = format.Head(r.log.Records[:position+1]) == an.head
		}
		r.anchors = append(r.anchors, an)
		witness, named := r.root.witness(an.key)
		from, revoked := r.compromised[an.key]
		if !named || an.log != r.root.Log || revoked && position >= from {
			continue
		}
		an.witness = witness
		byWitness[witness] = append(byWitness[witness], an)
	}
	var equivocations []Equivocation
	for _, witness := range sortedKeys(byWitness) {
		naming := map[string][]*anchor{}
		for _, a := range byWitness[witness] {
			naming[a.previous] = append(naming[a.previous], a)
		}
		var last *anchor
		for {
			previous := ""
			if last != nil {
				previous = last.identity
			}
			next := naming[previous]
			if len(next) > 1 {
				sortAnchors(next)
				eq := Equivocation{Witness: witness, Previous: previous}
				for _, a := range next {
					eq.Anchors = append(eq.Anchors, a.identity)
				}
				equivocations = append(equivocations, eq)
				break
			}
			if len(next) == 0 {
				break
			}
			a := next[0]
			if last != nil && (a.position < last.position || a.observed.Before(last.observed) || a.extended && !last.extended) {
				break
			}
			a.recognized = true
			last = a
		}
	}
	sortAnchors(r.anchors)
	for _, a := range r.anchors {
		switch {
		case !a.recognized:
			res.NotRecognized = append(res.NotRecognized, a.identity)
		case a.position < int64(r.n) && !a.extended:
			res.Recognized = append(res.Recognized, a.identity)
			res.NotExtended = append(res.NotExtended, a.identity)
		default:
			res.Recognized = append(res.Recognized, a.identity)
		}
	}
	sort.SliceStable(equivocations, func(i, j int) bool { return equivocations[i].Witness < equivocations[j].Witness })
	res.Equivocations = equivocations
	return res
}

// judgeRead finds whether the read is behind, where it ends and where a break starts, and the
// positions whose time the recognized anchors the log extends bound with a lower bound later than
// the upper. An anchor beyond the read bounds no time in it.
func (r *run) judgeRead() ReadResult {
	res := ReadResult{Behind: r.ended, Disagreeing: []int64{}}
	bootstrapEnd := int64(len(r.root.Bootstrap) - 1)
	// lastExtended is, for each witness, the last recognized anchor of it that the log extends.
	lastExtended := map[string]int64{}
	for _, a := range r.anchors {
		if a.recognized && a.extended {
			if p, ok := lastExtended[a.witness]; !ok || a.position > p {
				lastExtended[a.witness] = a.position
			}
		}
	}
	after := func(witness string) int64 {
		if p, ok := lastExtended[witness]; ok {
			return p
		}
		return bootstrapEnd
	}
	earliest := func(p *int64, q int64) *int64 {
		if p == nil || q < *p {
			return &q
		}
		return p
	}
	for _, a := range r.anchors {
		switch {
		case !a.recognized:
		case a.position >= int64(r.n):
			res.Behind = true
			res.EndsAfter = earliest(res.EndsAfter, after(a.witness))
		case !a.extended:
			res.BreakAfter = earliest(res.BreakAfter, after(a.witness))
		}
	}
	if res.EndsAfter != nil && *res.EndsAfter >= int64(r.n)-1 {
		res.EndsAfter = nil
	}
	for p := int64(0); p < int64(r.n); p++ {
		lower, upper, hasLower, hasUpper := r.bounds(p)
		if hasLower && hasUpper && lower.After(upper) {
			res.Disagreeing = append(res.Disagreeing, p)
		}
	}
	return res
}

// bounds gives a position's time: the latest time a recognized anchor the log extends observed at
// an earlier position, and the earliest one such an anchor observed at or after it.
func (r *run) bounds(p int64) (lower, upper time.Time, hasLower, hasUpper bool) {
	for _, a := range r.anchors {
		if !a.recognized || !a.extended {
			continue
		}
		if a.position < p && (!hasLower || a.observed.After(lower)) {
			lower, hasLower = a.observed, true
		}
		if a.position >= p && (!hasUpper || a.observed.Before(upper)) {
			upper, hasUpper = a.observed, true
		}
	}
	return lower, upper, hasLower, hasUpper
}

func sortAnchors(as []*anchor) {
	sort.SliceStable(as, func(i, j int) bool {
		if as[i].position != as[j].position {
			return as[i].position < as[j].position
		}
		return as[i].identity < as[j].identity
	})
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
