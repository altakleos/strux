package verify

import (
	"errors"
	"fmt"
	"strings"

	"github.com/altakleos/strux/format"
)

// Inputs are what verification reads: the initial trust root, how much of the log was read, the
// anchors read from where the trust root keeps them, and the reliance, if the run relies on a use.
type Inputs struct {
	TrustRoot any    // the initial trust root's content
	Log       string // the identity of the log read
	Size      int    // how many entries were read, from position 0
	Ended     bool   // a storage error ended the read before the log's head
	Anchors   [][]byte
	Reliance  *Reliance
}

// Reliance is the use relied on, for the revision its subject names, with its state read before a
// position: the number of entries read where none is given.
type Reliance struct {
	Use      string
	Subject  format.Reference
	Position *int
}

// Result is what verification returns, field by field as the kernel's verify types it.
type Result struct {
	Recognized    []int64             `json:"recognized"`
	NotRecognized []NotRecognizedFact `json:"not_recognized"`
	Pending       []PendingFact       `json:"pending"`
	Indeterminate []IndeterminateFact `json:"indeterminate"`
	Invalid       []InvalidEntry      `json:"invalid"`
	Anchors       AnchorsResult       `json:"anchors"`
	Read          ReadResult          `json:"read"`
	Reliance      *RelianceResult     `json:"reliance,omitempty"`
	Limits        []Limit             `json:"limits"`
}

// NotRecognizedFact is a fact that is not recognized, with what failed.
type NotRecognizedFact struct {
	Position int64     `json:"position"`
	Failed   []string  `json:"failed"`
	Unknown  []string  `json:"unknown"`
	Conflict *Conflict `json:"conflict,omitempty"`
}

// PendingFact is a fact that is pending.
type PendingFact struct {
	Position int64    `json:"position"`
	Unknown  []string `json:"unknown"`
}

// IndeterminateFact is a fact that a limit leaves undecided.
type IndeterminateFact struct {
	Position int64    `json:"position"`
	Limits   []string `json:"limits"`
	Unknown  []string `json:"unknown"`
}

// InvalidEntry is an entry the encoding refuses, with what it refuses.
type InvalidEntry struct {
	Position int64  `json:"position"`
	Refused  string `json:"refused"`
}

// Limit is one limit a run met.
type Limit struct {
	Kind     string `json:"kind"`
	Position *int64 `json:"position,omitempty"`
}

// Verify verifies a log: it reads the entries, judges the chain acts and the anchors, decides every
// fact in log order and, for a reliance, whether the use holds for its subject. It refuses a log
// other than the one the trust root names, and a reliance on a subject that is no valid entry read
// before its position, on a use that the subject's kind does not declare, or at a position past the
// entries read.
func Verify(l *Log, in Inputs) (*Result, error) {
	root, err := ReadTrustRoot(in.TrustRoot)
	if err != nil {
		return nil, err
	}
	if in.Log != root.Log {
		return nil, errors.New("the log read is not the one the trust root names")
	}
	r, err := newRun(l, root, in.Size, in.Ended)
	if err != nil {
		return nil, err
	}
	if err := r.judgeChainActs(); err != nil {
		return nil, err
	}
	anchors := r.judgeAnchors(in.Anchors)
	u, pkgs, err := r.bootstrapPackages()
	if err != nil {
		return nil, err
	}
	r.decideFacts(u, pkgs)
	res := &Result{Anchors: emptied(anchors), Read: r.readResult, Recognized: []int64{}, NotRecognized: []NotRecognizedFact{}, Pending: []PendingFact{}, Indeterminate: []IndeterminateFact{}, Invalid: []InvalidEntry{}, Limits: []Limit{}}
	lists := r.lists()
	res.Recognized = append(res.Recognized, lists.Recognized...)
	for _, f := range lists.NotRecognized {
		res.NotRecognized = append(res.NotRecognized, NotRecognizedFact{Position: int64(f.pos), Failed: orNone(f.failed), Unknown: orNone(f.unknown), Conflict: f.conflict})
	}
	for _, f := range lists.Pending {
		res.Pending = append(res.Pending, PendingFact{Position: int64(f.pos), Unknown: orNone(f.unknown)})
	}
	for _, f := range lists.Indeterminate {
		res.Indeterminate = append(res.Indeterminate, IndeterminateFact{Position: int64(f.pos), Limits: orNone(f.limits), Unknown: orNone(f.unknown)})
	}
	for _, e := range lists.Invalid {
		res.Invalid = append(res.Invalid, InvalidEntry{Position: int64(e.Position), Refused: e.Refused})
	}
	if r.readResult.Behind {
		res.Limits = append(res.Limits, Limit{Kind: "read-behind"})
	}
	if in.Reliance != nil {
		at := in.Size
		if in.Reliance.Position != nil {
			at = *in.Reliance.Position
		}
		if at > in.Size {
			return nil, fmt.Errorf("the reliance's position %d lies past the %d entries read", at, in.Size)
		}
		subject := r.factByIdentity(in.Reliance.Subject.Fact, at)
		if subject == nil {
			return nil, errors.New("the reliance's subject is no valid entry read before its position")
		}
		kind, _, ok := strings.Cut(in.Reliance.Use, ".uses.")
		if !ok || kind != subject.kind || mapOf(r.definition(subject)["uses"])[strings.TrimPrefix(in.Reliance.Use, kind+".uses.")] == nil {
			return nil, fmt.Errorf("the subject is a %s, which does not declare %s", subject.kind, in.Reliance.Use)
		}
		reliance := r.rely(in.Reliance.Use, in.Reliance.Subject, at)
		res.Reliance = &reliance
	}
	return res, nil
}

func orNone(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// emptied gives an anchors result its empty lists, so that it is written as the kernel types it.
func emptied(a AnchorsResult) AnchorsResult {
	a.Recognized, a.NotRecognized, a.NotExtended = orNone(a.Recognized), orNone(a.NotRecognized), orNone(a.NotExtended)
	if a.Equivocations == nil {
		a.Equivocations = []Equivocation{}
	}
	return a
}
