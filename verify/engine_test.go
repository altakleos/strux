package verify

import (
	"slices"
	"testing"

	"github.com/altakleos/strux/format"
)

// decided runs every step before the reliance over a vector's first case, read up to size
// entries, and returns the run.
func decidedRun(t *testing.T, vector string, size int, anchors bool) *run {
	t.Helper()
	l := vectors(t)[vector]
	c := cases(t, vector)[0]
	root, err := ReadTrustRoot(c.inputs["trust_root_configuration"].(map[string]any)["initial"])
	if err != nil {
		t.Fatal(err)
	}
	r, err := newRun(l, root, size, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.judgeChainActs(); err != nil {
		t.Fatal(err)
	}
	var read [][]byte
	if anchors {
		byIdentity := anchorBytes(t, l)
		for _, id := range c.anchors {
			read = append(read, byIdentity[id])
		}
	}
	r.judgeAnchors(read)
	if _, err := r.decideLog(); err != nil {
		t.Fatal(err)
	}
	return r
}

// A location is one line or a range of lines, numbered from 1 and written without sign or
// leading zero.
func TestALocationIsALineOrARange(t *testing.T) {
	for location, want := range map[string][2]int{"L1": {1, 1}, "L52": {52, 52}, "L3-L7": {3, 7}, "L4-L4": {4, 4}} {
		first, last, ok := lineRange(location)
		if !ok || first != want[0] || last != want[1] {
			t.Errorf("%s: got %d, %d, %v", location, first, last, ok)
		}
	}
	for _, location := range []string{"L0", "L+1", "L01", "L1-L+2", "L3-L2", "3", "L3-", "l3", "L3-4"} {
		if _, _, ok := lineRange(location); ok {
			t.Errorf("%s reads as a location", location)
		}
	}
}

// The fact being decided carries its own bytes, in a second pass as in the first, though the
// pass decides it before holding it.
func TestTheBytesOfTheFactDecidedAreReadInEveryPass(t *testing.T) {
	r := decidedRun(t, "quoted-bytes", 13, true)
	carrier := r.facts[10]
	digest := str(carrier.content["bytes"])
	again := &fact{pos: carrier.pos, entry: carrier.entry, p: carrier.p, kind: carrier.kind, pkg: carrier.pkg, def: carrier.def, content: carrier.content, instance: carrier.instance}
	second := *r
	second.facts = make([]*fact, len(r.facts))
	copy(second.facts, r.facts[:carrier.pos])
	for _, x := range []reader{{r: r, pos: carrier.pos, self: carrier}, {r: &second, pos: carrier.pos, self: again}} {
		if res := x.Retained(digest); res.Value != "true" {
			t.Errorf("the bytes the fact carries: got %+v", res)
		}
	}
	// Before the fact that carries them, and with only a fact that is not recognized carrying
	// them, the bytes are not retained.
	if res := (reader{r: r, pos: 9}).Retained(digest); res.Value != "false" {
		t.Errorf("before the carrier: got %+v", res)
	}
}

// While a configuration that would activate a package is pending, a fact under that package waits
// with it.
func TestAFactWaitsOnTheConfigurationThatWouldActivateIt(t *testing.T) {
	r := decidedRun(t, "inactive-package", 17, false)
	activation, entry := r.facts[12], r.facts[13]
	if activation.status != Pending {
		t.Fatalf("the configuration without an anchor: got %v", activation.status)
	}
	if entry.status != Pending || !slices.Contains(entry.unknown, recognizeCondition("package-active")) {
		t.Errorf("the entry under the package it would activate: got %v, unknown %v, failed %v", entry.status, entry.unknown, entry.failed)
	}
	// The note under the package the bootstrap activates, which needs no deadline, does not wait.
	if r.facts[14].status != Recognized {
		t.Errorf("the note under the active package: got %v, unknown %v", r.facts[14].status, r.facts[14].unknown)
	}
}

// A decision is in force only at positions after the one that records it, and asking of a later
// one says so.
func TestADecisionIsInForceOnlyAfterItsPosition(t *testing.T) {
	r := decidedRun(t, "quoted-bytes", 13, true)
	later := r.facts[10]
	res := reader{r: r, pos: 8}.InForce(format.Reference{Fact: later.entry.Fact.Identity, Digest: str(later.p["content"])})
	if res.Value != "false" {
		t.Errorf("a decision recorded after the position: got %+v", res)
	}
}
