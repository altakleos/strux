// Package verify implements verification of a log: which of its entries are facts, which facts
// are recognized, and whether a use holds for a revision when someone relies on it.
package verify

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/altakleos/strux/format"
)

// Log is a log as the encoding exchanges it: its records in append order, the bytes they carry,
// each under the digest of its SHA-256, and the anchors read from where the witnesses keep them.
type Log struct {
	Records [][]byte
	Blobs   map[string][]byte // by digest, as "sha256:" and the lowercase hexadecimal
	Anchors [][]byte
}

// ReadLog reads a log in the interchange layout: records.jsonl, a record to each line in append
// order; anchors.jsonl, an anchor to each line; and blobs, each file named by the hexadecimal of
// the SHA-256 its name claims. A blob whose bytes do not have that digest is kept under its name,
// so an entry that names it reads it as absent, as the encoding says of bytes that do not match.
func ReadLog(fsys fs.FS) (*Log, error) {
	records, err := fs.ReadFile(fsys, "records.jsonl")
	if err != nil {
		return nil, err
	}
	l := &Log{Records: format.Records(records), Blobs: map[string][]byte{}}
	switch anchors, err := fs.ReadFile(fsys, "anchors.jsonl"); {
	case err == nil:
		l.Anchors = format.Records(anchors)
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	blobs, err := fs.ReadDir(fsys, "blobs")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, b := range blobs {
		if b.IsDir() {
			return nil, fmt.Errorf("blobs/%s: a blob is a file", b.Name())
		}
		data, err := fs.ReadFile(fsys, "blobs/"+b.Name())
		if err != nil {
			return nil, err
		}
		l.Blobs["sha256:"+b.Name()] = data
	}
	return l, nil
}

// Entry is what the first step of verification makes of a record: the fact it holds, or, for an
// invalid entry, what the encoding refuses of it.
type Entry struct {
	Position int
	Fact     *format.Entry // nil for an invalid entry
	Refused  string        // what the encoding refuses, for an invalid entry
	Signers  []string      // the keys whose signatures the record holds, for a fact
}

// ReadEntries reads the first n records of a log, as the fact each holds or as an invalid entry,
// for the log that the trust root names. An entry carries the content bytes its fact names by
// digest; where the record is too broken to name any, the encoding refuses the record first.
func (l *Log) ReadEntries(log string, n int) []Entry {
	out := make([]Entry, 0, n)
	for i, record := range l.Records[:min(n, len(l.Records))] {
		e, err := format.ReadEntry(record, l.Blobs[contentDigest(record)], log)
		var refusal *format.Refusal
		switch {
		case err == nil:
			out = append(out, Entry{Position: i, Fact: e, Signers: signers(record)})
		case errors.As(err, &refusal):
			out = append(out, Entry{Position: i, Refused: refusal.What})
		default:
			out = append(out, Entry{Position: i, Refused: format.RefusedRecord})
		}
	}
	return out
}

// contentDigest is the content digest that a record's signed object names, if it names one.
func contentDigest(record []byte) string {
	v, err := format.ParseJSON(record)
	if err != nil {
		return ""
	}
	top, _ := v.(map[string]any)
	fact, _ := top["fact"].(map[string]any)
	digest, _ := fact["content"].(string)
	if !strings.HasPrefix(digest, "sha256:") {
		return ""
	}
	return digest
}

// signers are the keys a record's signatures name, in the order the record holds them.
func signers(record []byte) []string {
	v, err := format.ParseJSON(record)
	if err != nil {
		return nil
	}
	top, _ := v.(map[string]any)
	sigs, _ := top["signatures"].([]any)
	var out []string
	for _, s := range sigs {
		m, _ := s.(map[string]any)
		if key, ok := m["key"].(string); ok {
			out = append(out, key)
		}
	}
	return out
}
