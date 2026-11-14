package verify

import (
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/altakleos/strux/format"
)

// vectorCase is one case of a verification vector: the inputs of verify, and the fields of the
// result that a conforming verifier returns for them.
type vectorCase struct {
	vector  string
	name    string
	inputs  map[string]any
	anchors []string // the identities of the anchors the case reads
	want    map[string]any
}

// vectors reads every verification vector in testdata/vectors: each a log in the interchange
// layout, with its cases beside it in cases.json.
func vectors(t *testing.T) map[string]*Log {
	t.Helper()
	root := os.DirFS("testdata/vectors")
	dirs, err := fs.ReadDir(root, ".")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*Log{}
	for _, d := range dirs {
		sub, err := fs.Sub(root, d.Name())
		if err != nil {
			t.Fatal(err)
		}
		l, err := ReadLog(sub)
		if err != nil {
			t.Fatalf("%s: %v", d.Name(), err)
		}
		out[d.Name()] = l
	}
	if len(out) == 0 {
		t.Fatal("testdata/vectors holds no vector")
	}
	return out
}

func cases(t *testing.T, vector string) []vectorCase {
	t.Helper()
	data, err := fs.ReadFile(os.DirFS("testdata/vectors"), path.Join(vector, "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := format.ParseJSON(data)
	if err != nil {
		t.Fatalf("%s: cases.json: %v", vector, err)
	}
	var out []vectorCase
	for _, c := range v.(map[string]any)["cases"].([]any) {
		m := c.(map[string]any)
		var anchors []string
		for _, a := range m["anchors"].([]any) {
			anchors = append(anchors, a.(string))
		}
		out = append(out, vectorCase{vector: vector, name: m["case"].(string), inputs: m["inputs"].(map[string]any), anchors: anchors, want: m["want"].(map[string]any)})
	}
	return out
}

// The first step reads each entry: a fact, or an invalid entry with what the encoding refuses of
// it. Every case's invalid entries are the ones the vector states.
func TestEntriesAreTheFactsAndInvalidEntriesTheVectorsState(t *testing.T) {
	for name, l := range vectors(t) {
		for _, c := range cases(t, name) {
			read := c.inputs["log"].(map[string]any)
			n := int(read["size"].(int64))
			entries := l.ReadEntries(read["log"].(string), n)
			if len(entries) != n {
				t.Errorf("%s/%s: read %d entries, want %d", name, c.name, len(entries), n)
			}
			stated, ok := c.want["invalid"].([]any)
			if !ok {
				continue
			}
			want := map[int64]string{}
			for _, item := range stated {
				m := item.(map[string]any)
				want[m["position"].(int64)] = m["refused"].(string)
			}
			for _, e := range entries {
				if got, stated := e.Refused, want[int64(e.Position)]; got != stated {
					t.Errorf("%s/%s: the entry at %d is refused for %q, and the vector states %q", name, c.name, e.Position, got, stated)
				}
			}
		}
	}
}

// A log without anchors or blobs is still a log, and a record the encoding cannot read is an
// invalid entry, refused as a record.
func TestALogNeedsOnlyItsRecords(t *testing.T) {
	l, err := ReadLog(fstest.MapFS{"records.jsonl": {Data: []byte("not a record\n")}})
	if err != nil {
		t.Fatal(err)
	}
	entries := l.ReadEntries("sha256:00", 5)
	if len(entries) != 1 || entries[0].Fact != nil || entries[0].Refused != format.RefusedRecord {
		t.Errorf("got %+v, want one entry refused as a record", entries)
	}
}

// stated reads the identities a stated result lists.
func stated(v any) []string {
	out := []string{}
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func orEmpty(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func sameList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The first two steps judge the chain acts and the anchors: which anchors are recognized, which
// the log does not extend, the equivocations, whether the read is behind and where it ends, where
// a break starts, and which positions the anchors bound inconsistently.
func TestAnchorsAndTheReadAreAsTheVectorsState(t *testing.T) {
	for name, l := range vectors(t) {
		byIdentity := map[string][]byte{}
		for _, b := range l.Anchors {
			identity, _, err := format.ReadAnchor(b)
			if err != nil {
				t.Fatalf("%s: an anchor of the vector does not read: %v", name, err)
			}
			byIdentity[identity] = b
		}
		for _, c := range cases(t, name) {
			where := name + "/" + c.name
			read := c.inputs["log"].(map[string]any)
			root, err := ReadTrustRoot(c.inputs["trust_root_configuration"].(map[string]any)["initial"])
			if err != nil {
				t.Fatalf("%s: %v", where, err)
			}
			ended, _ := read["ended"].(bool)
			r, err := newRun(l, root, int(read["size"].(int64)), ended)
			if err != nil {
				t.Fatalf("%s: %v", where, err)
			}
			if err := r.judgeChainActs(); err != nil {
				t.Fatalf("%s: %v", where, err)
			}
			var anchors [][]byte
			for _, id := range c.anchors {
				anchors = append(anchors, byIdentity[id])
			}
			got := r.judgeAnchors(anchors)
			if want, ok := c.want["anchors"].(map[string]any); ok {
				for field, lists := range map[string][2][]string{
					"recognized":     {orEmpty(got.Recognized), stated(want["recognized"])},
					"not_recognized": {orEmpty(got.NotRecognized), stated(want["not_recognized"])},
					"not_extended":   {orEmpty(got.NotExtended), stated(want["not_extended"])},
				} {
					if !sameList(lists[0], lists[1]) {
						t.Errorf("%s: anchors.%s is %q, and the vector states %q", where, field, lists[0], lists[1])
					}
				}
				if int64(got.Unreadable) != want["unreadable"].(int64) {
					t.Errorf("%s: anchors.unreadable is %d, and the vector states %d", where, got.Unreadable, want["unreadable"])
				}
				var eqs []string
				for _, e := range got.Equivocations {
					eqs = append(eqs, e.Witness+" "+e.Previous+" "+strings.Join(e.Anchors, ","))
				}
				var wantEqs []string
				for _, e := range want["equivocations"].([]any) {
					m := e.(map[string]any)
					previous, _ := m["previous"].(string)
					wantEqs = append(wantEqs, m["witness"].(string)+" "+previous+" "+strings.Join(stated(m["anchors"]), ","))
				}
				if !sameList(orEmpty(eqs), orEmpty(wantEqs)) {
					t.Errorf("%s: anchors.equivocations are %q, and the vector states %q", where, eqs, wantEqs)
				}
			}
			if want, ok := c.want["read"].(map[string]any); ok {
				gotRead := r.judgeRead()
				if gotRead.Behind != want["behind"].(bool) {
					t.Errorf("%s: read.behind is %v, and the vector states %v", where, gotRead.Behind, want["behind"])
				}
				for field, got := range map[string]*int64{"ends_after": gotRead.EndsAfter, "break_after": gotRead.BreakAfter} {
					w, stated := want[field].(int64)
					switch {
					case stated != (got != nil):
						t.Errorf("%s: read.%s is %v, and the vector states %v", where, field, got, want[field])
					case stated && *got != w:
						t.Errorf("%s: read.%s is %d, and the vector states %d", where, field, *got, w)
					}
				}
				var disagreeing []string
				for _, p := range gotRead.Disagreeing {
					disagreeing = append(disagreeing, strconv.FormatInt(p, 10))
				}
				var wantDisagreeing []string
				for _, p := range want["disagreeing"].([]any) {
					wantDisagreeing = append(wantDisagreeing, strconv.FormatInt(p.(int64), 10))
				}
				if !sameList(orEmpty(disagreeing), orEmpty(wantDisagreeing)) {
					t.Errorf("%s: read.disagreeing is %q, and the vector states %q", where, disagreeing, wantDisagreeing)
				}
			}
		}
	}
}

// prepare runs the first steps of a case: the entries, the chain acts, the anchors and the read.
func prepare(t *testing.T, l *Log, c vectorCase, byIdentity map[string][]byte) *run {
	t.Helper()
	where := c.vector + "/" + c.name
	read := c.inputs["log"].(map[string]any)
	root, err := ReadTrustRoot(c.inputs["trust_root_configuration"].(map[string]any)["initial"])
	if err != nil {
		t.Fatalf("%s: %v", where, err)
	}
	ended, _ := read["ended"].(bool)
	r, err := newRun(l, root, int(read["size"].(int64)), ended)
	if err != nil {
		t.Fatalf("%s: %v", where, err)
	}
	if err := r.judgeChainActs(); err != nil {
		t.Fatalf("%s: %v", where, err)
	}
	var anchors [][]byte
	for _, id := range c.anchors {
		anchors = append(anchors, byIdentity[id])
	}
	r.judgeAnchors(anchors)
	return r
}

func anchorBytes(t *testing.T, l *Log) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, b := range l.Anchors {
		identity, _, err := format.ReadAnchor(b)
		if err != nil {
			t.Fatal(err)
		}
		out[identity] = b
	}
	return out
}

func describeFact(f *fact) string {
	s := strconv.Itoa(f.pos) + " failed=" + strings.Join(f.failed, ",") + " unknown=" + strings.Join(f.unknown, ",") + " limits=" + strings.Join(f.limits, ",")
	if f.conflict != nil {
		s += " conflict=" + strconv.FormatInt(f.conflict.With, 10) + "/" + strconv.FormatBool(f.conflict.Copy)
	}
	return s
}

func describeStated(item any) string {
	m := item.(map[string]any)
	join := func(key string) string {
		v, ok := m[key].([]any)
		if !ok {
			return ""
		}
		return strings.Join(stated(v), ",")
	}
	s := strconv.FormatInt(m["position"].(int64), 10) + " failed=" + join("failed") + " unknown=" + join("unknown") + " limits=" + join("limits")
	if c, ok := m["conflict"].(map[string]any); ok {
		s += " conflict=" + strconv.FormatInt(c["with"].(int64), 10) + "/" + strconv.FormatBool(c["copy"].(bool))
	}
	return s
}

// The facts are decided in log order, and every entry read has the status the vector states:
// recognized, not recognized with what failed or is unknown, pending, indeterminate with the limits
// in its way, or invalid.
func TestEveryEntryHasTheStatusTheVectorsState(t *testing.T) {
	for name, l := range vectors(t) {
		byIdentity := anchorBytes(t, l)
		for _, c := range cases(t, name) {
			where := name + "/" + c.name
			if _, stated := c.want["recognized"]; !stated {
				continue
			}
			r := prepare(t, l, c, byIdentity)
			u, pkgs, err := r.bootstrapPackages()
			if err != nil {
				t.Fatalf("%s: %v", where, err)
			}
			r.decideFacts(u, pkgs)
			got := r.lists()
			var recognized []string
			for _, p := range got.Recognized {
				recognized = append(recognized, strconv.FormatInt(p, 10))
			}
			var wantRecognized []string
			for _, p := range c.want["recognized"].([]any) {
				wantRecognized = append(wantRecognized, strconv.FormatInt(p.(int64), 10))
			}
			if !sameList(orEmpty(recognized), orEmpty(wantRecognized)) {
				t.Errorf("%s: recognized %q, and the vector states %q", where, recognized, wantRecognized)
			}
			for field, facts := range map[string][]*fact{"not_recognized": got.NotRecognized, "pending": got.Pending, "indeterminate": got.Indeterminate} {
				var have, want []string
				for _, f := range facts {
					have = append(have, describeFact(f))
				}
				for _, item := range c.want[field].([]any) {
					want = append(want, describeStated(item))
				}
				if !sameList(orEmpty(have), orEmpty(want)) {
					t.Errorf("%s: %s\n  got  %q\n  want %q", where, field, have, want)
				}
			}
		}
	}
}
