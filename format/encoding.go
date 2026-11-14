package format

import (
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"filippo.io/edwards25519"
)

// This file implements the encoding of entries, anchors and signatures of interpretation revision
// 1: the values a verifier parses, the spellings of times,
// keys and signatures, Ed25519 as ZIP-215 verifies it, records, the head and anchors, and the
// interchange layout of a log's records.

var (
	digestSpelling    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	keySpelling       = regexp.MustCompile(`^ed25519:[0-9a-f]{64}$`)
	signatureSpelling = regexp.MustCompile(`^[0-9a-f]{128}$`)
	// timeSpelling is the shape of a time: whole seconds, then a fraction of one to nine digits
	// that does not end in zero, then Z. ParseTime checks the rest.
	timeSpelling = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{0,8}[1-9])?Z$`)
)

// maxJSONDepth bounds how deeply ParseJSON nests, so a hostile text cannot exhaust the stack.
const maxJSONDepth = 1000

// ParseJSON reads bytes as an I-JSON text (RFC 7493) whose value lies in the data model: objects
// without a duplicate member, arrays, strings of Unicode scalar values that are no noncharacter,
// booleans, and integers within ±(2⁵³−1). It refuses invalid UTF-8, an escaped lone surrogate, null,
// and a number with a fraction or an exponent. Insignificant whitespace is allowed; ReadCanonical
// also requires the canonical form.
func ParseJSON(b []byte) (any, error) {
	if !utf8.Valid(b) {
		return nil, errors.New("not valid UTF-8")
	}
	r := &jsonReader{b: b}
	r.space()
	v, err := r.value(0)
	if err != nil {
		return nil, err
	}
	r.space()
	if r.i != len(b) {
		return nil, fmt.Errorf("byte %d: text after the value", r.i)
	}
	return v, nil
}

// ReadCanonical reads bytes that must be the JCS form (RFC 8785) of a value in the data model, and
// returns the value.
func ReadCanonical(b []byte) (any, error) {
	v, err := ParseJSON(b)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(Canonical(v), b) {
		return nil, errors.New("not the canonical form of its value")
	}
	return v, nil
}

type jsonReader struct {
	b []byte
	i int
}

func (r *jsonReader) space() {
	for r.i < len(r.b) && (r.b[r.i] == ' ' || r.b[r.i] == '\t' || r.b[r.i] == '\n' || r.b[r.i] == '\r') {
		r.i++
	}
}

func (r *jsonReader) fail(format string, args ...any) error {
	return fmt.Errorf("byte %d: %s", r.i, fmt.Sprintf(format, args...))
}

func (r *jsonReader) value(depth int) (any, error) {
	if depth > maxJSONDepth {
		return nil, r.fail("nested more than %d deep", maxJSONDepth)
	}
	if r.i >= len(r.b) {
		return nil, r.fail("a value is missing")
	}
	switch c := r.b[r.i]; {
	case c == '{':
		r.i++
		obj := map[string]any{}
		r.space()
		if r.i < len(r.b) && r.b[r.i] == '}' {
			r.i++
			return obj, nil
		}
		for {
			r.space()
			if r.i >= len(r.b) || r.b[r.i] != '"' {
				return nil, r.fail("a member name is missing")
			}
			name, err := r.str()
			if err != nil {
				return nil, err
			}
			if _, dup := obj[name]; dup {
				return nil, r.fail("member %q appears twice", name)
			}
			r.space()
			if r.i >= len(r.b) || r.b[r.i] != ':' {
				return nil, r.fail("a colon is missing")
			}
			r.i++
			r.space()
			v, err := r.value(depth + 1)
			if err != nil {
				return nil, err
			}
			obj[name] = v
			r.space()
			if r.i < len(r.b) && r.b[r.i] == ',' {
				r.i++
				continue
			}
			if r.i < len(r.b) && r.b[r.i] == '}' {
				r.i++
				return obj, nil
			}
			return nil, r.fail("an object is not closed")
		}
	case c == '[':
		r.i++
		list := []any{}
		r.space()
		if r.i < len(r.b) && r.b[r.i] == ']' {
			r.i++
			return list, nil
		}
		for {
			r.space()
			v, err := r.value(depth + 1)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
			r.space()
			if r.i < len(r.b) && r.b[r.i] == ',' {
				r.i++
				continue
			}
			if r.i < len(r.b) && r.b[r.i] == ']' {
				r.i++
				return list, nil
			}
			return nil, r.fail("an array is not closed")
		}
	case c == '"':
		return r.str()
	case bytes.HasPrefix(r.b[r.i:], []byte("true")):
		r.i += 4
		return true, nil
	case bytes.HasPrefix(r.b[r.i:], []byte("false")):
		r.i += 5
		return false, nil
	case bytes.HasPrefix(r.b[r.i:], []byte("null")):
		return nil, r.fail("null lies outside the data model")
	case c == '-' || (c >= '0' && c <= '9'):
		return r.number()
	}
	return nil, r.fail("unexpected %q", r.b[r.i])
}

func (r *jsonReader) number() (any, error) {
	start := r.i
	if r.b[r.i] == '-' {
		r.i++
	}
	digitsAt := r.i
	for r.i < len(r.b) && r.b[r.i] >= '0' && r.b[r.i] <= '9' {
		r.i++
	}
	switch {
	case r.i == digitsAt:
		return nil, r.fail("a number has no digits")
	case r.b[digitsAt] == '0' && r.i-digitsAt > 1:
		return nil, r.fail("a number has a leading zero")
	}
	if r.i < len(r.b) && (r.b[r.i] == '.' || r.b[r.i] == 'e' || r.b[r.i] == 'E') {
		return nil, r.fail("a number with a fraction or an exponent lies outside the data model")
	}
	n, err := strconv.ParseInt(string(r.b[start:r.i]), 10, 64)
	if err != nil || n > maxInteger || n < -maxInteger {
		return nil, r.fail("integer %s lies outside ±(2⁵³−1)", r.b[start:r.i])
	}
	return n, nil
}

func (r *jsonReader) str() (string, error) {
	r.i++ // the opening quote
	var b strings.Builder
	for {
		if r.i >= len(r.b) {
			return "", r.fail("a string is not closed")
		}
		c := r.b[r.i]
		switch {
		case c == '"':
			r.i++
			return b.String(), nil
		case c < 0x20:
			return "", r.fail("a control character must be escaped")
		case c == '\\':
			if r.i+1 >= len(r.b) {
				return "", r.fail("an escape is not complete")
			}
			e := r.b[r.i+1]
			r.i += 2
			switch e {
			case '"', '\\', '/':
				b.WriteByte(e)
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case 'u':
				cp, err := r.hex4()
				if err != nil {
					return "", err
				}
				switch {
				case cp >= 0xDC00 && cp <= 0xDFFF:
					return "", r.fail("an escaped lone surrogate")
				case cp >= 0xD800 && cp <= 0xDBFF:
					if r.i+1 >= len(r.b) || r.b[r.i] != '\\' || r.b[r.i+1] != 'u' {
						return "", r.fail("an escaped lone surrogate")
					}
					r.i += 2
					low, err := r.hex4()
					if err != nil {
						return "", err
					}
					if low < 0xDC00 || low > 0xDFFF {
						return "", r.fail("an escaped lone surrogate")
					}
					cp = 0x10000 + (cp-0xD800)<<10 + (low - 0xDC00)
				}
				if noncharacter(cp) {
					return "", r.fail("a noncharacter")
				}
				b.WriteRune(cp)
			default:
				return "", r.fail("unknown escape \\%c", e)
			}
		default:
			ch, size := utf8.DecodeRune(r.b[r.i:])
			if noncharacter(ch) {
				return "", r.fail("a noncharacter")
			}
			b.Write(r.b[r.i : r.i+size])
			r.i += size
		}
	}
}

func (r *jsonReader) hex4() (rune, error) {
	if r.i+4 > len(r.b) {
		return 0, r.fail("an escape is not complete")
	}
	var n rune
	for _, c := range r.b[r.i : r.i+4] {
		var digit rune
		switch {
		case c >= '0' && c <= '9':
			digit = rune(c - '0')
		case c >= 'a' && c <= 'f':
			digit = rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			digit = rune(c-'A') + 10
		default:
			return 0, r.fail("an escape is not hexadecimal")
		}
		n = n<<4 | digit
	}
	r.i += 4
	return n, nil
}

// noncharacter reports a Unicode noncharacter, which I-JSON excludes.
func noncharacter(c rune) bool {
	return (c >= 0xFDD0 && c <= 0xFDEF) || c&0xFFFE == 0xFFFE
}

// ParseTime reads a time in the one spelling the encoding fixes: as Go's time.RFC3339Nano writes
// a time in UTC, with a year from 0001 to 9999 and no second 60. Any other spelling is an error.
func ParseTime(s string) (time.Time, error) {
	if !timeSpelling.MatchString(s) {
		return time.Time{}, fmt.Errorf("time %q is not spelled YYYY-MM-DDTHH:MM:SS, a fraction with no trailing zero, and Z", s)
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("time %q: %w", s, err)
	}
	if t.Year() < 1 || t.UTC().Format(time.RFC3339Nano) != s {
		return time.Time{}, fmt.Errorf("time %q is not a time from year 0001 to 9999 in its one spelling", s)
	}
	return t, nil
}

// ell1 is ℓ−1, where ℓ is the order of Ed25519's prime-order subgroup, as a scalar.
var ell1 = func() *edwards25519.Scalar {
	b, _ := hex.DecodeString("ecd3f55c1a631258d69cf7a2def9de1400000000000000000000000000000010")
	s, err := edwards25519.NewScalar().SetCanonicalBytes(b)
	if err != nil {
		panic(err)
	}
	return s
}()

// ValidKey reads a key spelled ed25519: and 64 lowercase hexadecimal digits, and returns its point
// when it is the canonical encoding of a point A with [ℓ]A the identity and A not the identity.
func ValidKey(key string) (*edwards25519.Point, []byte, error) {
	if !keySpelling.MatchString(key) {
		return nil, nil, fmt.Errorf("key %q is not ed25519: and 64 lowercase hexadecimal digits", key)
	}
	raw, _ := hex.DecodeString(strings.TrimPrefix(key, "ed25519:"))
	A, err := new(edwards25519.Point).SetBytes(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("key %s encodes no point", key)
	}
	identity := edwards25519.NewIdentityPoint()
	if !bytes.Equal(A.Bytes(), raw) {
		return nil, nil, fmt.Errorf("key %s is not the canonical encoding of its point", key)
	}
	if A.Equal(identity) == 1 {
		return nil, nil, fmt.Errorf("key %s is the identity", key)
	}
	ellA := new(edwards25519.Point).ScalarMult(ell1, A)
	if ellA.Add(ellA, A).Equal(identity) != 1 {
		return nil, nil, fmt.Errorf("key %s lies outside the prime-order subgroup", key)
	}
	return A, raw, nil
}

// VerifySignature verifies a PureEdDSA Ed25519 signature, with no context, over the message as
// ZIP-215 verifies it: S below ℓ, any encoding of R, and the cofactored equation
// [8]([S]B − [k]A − R) = identity. It verifies only under a valid key (ValidKey).
func VerifySignature(key string, message []byte, signature string) error {
	A, keyBytes, err := ValidKey(key)
	if err != nil {
		return err
	}
	if !signatureSpelling.MatchString(signature) {
		return errors.New("a signature is not 128 lowercase hexadecimal digits")
	}
	sig, _ := hex.DecodeString(signature)
	R, err := new(edwards25519.Point).SetBytes(sig[:32])
	if err != nil {
		return errors.New("the signature's R encodes no point")
	}
	S, err := edwards25519.NewScalar().SetCanonicalBytes(sig[32:])
	if err != nil {
		return errors.New("the signature's S is not below the group order")
	}
	h := sha512.New()
	h.Write(sig[:32])
	h.Write(keyBytes)
	h.Write(message)
	k, err := edwards25519.NewScalar().SetUniformBytes(h.Sum(nil))
	if err != nil {
		return err
	}
	minusA := new(edwards25519.Point).Negate(A)
	check := new(edwards25519.Point).VarTimeDoubleScalarBaseMult(k, minusA, S)
	check.Subtract(check, R)
	check.MultByCofactor(check)
	if check.Equal(edwards25519.NewIdentityPoint()) != 1 {
		return fmt.Errorf("the signature does not verify under %s", key)
	}
	return nil
}

// Head is the head over the records, each leaf being a record's bytes: sha256: and the hexadecimal
// RFC 9162 Merkle tree hash. It is defined for one record or more.
func Head(records [][]byte) string {
	return "sha256:" + hex.EncodeToString(merkleTreeHash(records))
}

func merkleTreeHash(leaves [][]byte) []byte {
	switch len(leaves) {
	case 0:
		sum := sha256.Sum256(nil)
		return sum[:]
	case 1:
		sum := sha256.Sum256(append([]byte{0}, leaves[0]...))
		return sum[:]
	}
	k := 1
	for k*2 < len(leaves) {
		k *= 2
	}
	node := append([]byte{1}, merkleTreeHash(leaves[:k])...)
	sum := sha256.Sum256(append(node, merkleTreeHash(leaves[k:])...))
	return sum[:]
}

// Records reads a log's records.jsonl: each line that a line feed ends is one record's bytes, the
// line feed being no part of them, and a last line without one is no entry.
func Records(jsonl []byte) [][]byte {
	var out [][]byte
	for {
		i := bytes.IndexByte(jsonl, '\n')
		if i < 0 {
			return out
		}
		out = append(out, jsonl[:i])
		jsonl = jsonl[i+1:]
	}
}

// Entry is what verification reads of a valid entry.
type Entry struct {
	Identity string         // digest(P)
	Fact     map[string]any // P
	// ContentAbsent is true when the entry carries no content bytes, or bytes whose SHA-256 is not
	// the content digest P records: they count as absent, and reading them is a limit.
	ContentAbsent bool
}

// Refusal is why the encoding refuses an entry, which makes the entry invalid. What is the first
// of these that applies, in this order: the record, for bytes that break the encoding or a record
// or a signed object that is not the object the encoding fixes; the log, for a fact that names
// another log than the trust root's; a signature, for one that does not verify for the key it
// names; and the content, for content bytes that match their digest and are not the canonical form
// of a value.
type Refusal struct {
	What string
	Err  error
}

// What the encoding refuses of an entry, in the order in which a Refusal names the first.
const (
	RefusedRecord    = "record"
	RefusedLog       = "log"
	RefusedSignature = "signature"
	RefusedContent   = "content"
)

func (r *Refusal) Error() string { return r.Err.Error() }

func refuse(what string, err error) error { return &Refusal{What: what, Err: err} }

// ReadEntry reads an entry: a record's bytes, the content bytes it carries (nil when it carries
// none) and the log the trust root names. It returns a Refusal, which makes the entry invalid, when
// the record breaks the encoding, names another log or carries a signature that does not verify, or
// when content bytes whose SHA-256 is the content digest are not the JCS form of a value. Whatever
// a record must be is checked before the log it names, and the log before any signature, so that
// the refusal names the first of them that applies.
func ReadEntry(record, content []byte, log string) (*Entry, error) {
	p, signatures, err := readRecord(record)
	if err != nil {
		return nil, refuse(RefusedRecord, err)
	}
	if p["log"] != log {
		return nil, refuse(RefusedLog, fmt.Errorf("the fact names log %v, not the trust root's", p["log"]))
	}
	message := Canonical(p)
	for _, s := range signatures {
		if err := VerifySignature(s["key"].(string), message, s["sig"].(string)); err != nil {
			return nil, refuse(RefusedSignature, err)
		}
	}
	entry := &Entry{Identity: Digest(p), Fact: p, ContentAbsent: true}
	if content != nil {
		sum := sha256.Sum256(content)
		if "sha256:"+hex.EncodeToString(sum[:]) == p["content"] {
			if _, err := ReadCanonical(content); err != nil {
				return nil, refuse(RefusedContent, fmt.Errorf("the content: %w", err))
			}
			entry.ContentAbsent = false
		}
	}
	return entry, nil
}

// readRecord reads a record's bytes into its signed object and its signatures, and says what keeps
// them from being a record: bytes out of the canonical form, a record or a signed object that is
// not the object the encoding fixes, or signatures that are not in the form a record gives them.
// Whether a signature verifies is no part of that form.
func readRecord(record []byte) (map[string]any, []map[string]any, error) {
	v, err := ReadCanonical(record)
	if err != nil {
		return nil, nil, fmt.Errorf("the record: %w", err)
	}
	rec, ok := v.(map[string]any)
	if !ok || len(rec) != 2 || rec["fact"] == nil || rec["signatures"] == nil {
		return nil, nil, errors.New(`the record is not {"fact": P, "signatures": [...]}`)
	}
	p, ok := rec["fact"].(map[string]any)
	if !ok {
		return nil, nil, errors.New("the record's fact is not an object")
	}
	if err := checkFact(p); err != nil {
		return nil, nil, err
	}
	list, ok := rec["signatures"].([]any)
	if !ok || len(list) == 0 {
		return nil, nil, errors.New("the record's signatures are not a non-empty list")
	}
	signatures, previous := make([]map[string]any, 0, len(list)), ""
	for _, raw := range list {
		s, ok := raw.(map[string]any)
		key, _ := s["key"].(string)
		sig, _ := s["sig"].(string)
		if !ok || len(s) != 2 || !keySpelling.MatchString(key) || !signatureSpelling.MatchString(sig) {
			return nil, nil, errors.New(`a signature is not {"key": an ed25519: key, "sig": 128 hexadecimal digits}`)
		}
		if key <= previous {
			return nil, nil, errors.New("the signatures are not sorted by key, each key once")
		}
		signatures, previous = append(signatures, s), key
	}
	if key, named := p["key"]; named && (len(signatures) != 1 || signatures[0]["key"] != key) {
		return nil, nil, errors.New("a fact that names its key carries exactly one signature, which names that key")
	}
	return p, signatures, nil
}

// member is how one member of a closed object is read.
type member struct {
	required bool
	check    func(any) error
}

var factMembers = map[string]member{
	"strux":         {true, exactly("fact/1")},
	"log":           {true, isDigest},
	"kind":          {true, isString},
	"package":       {true, isDigest},
	"content":       {true, isDigest},
	"key":           {false, isKey},
	"captures":      {false, isCaptures},
	"expected":      {false, isDigest},
	"operation":     {false, isString},
	"operation_id":  {false, isString},
	"under":         {false, isRef},
	"append_by":     {false, isTime},
	"configuration": {false, isDigests},
}

var anchorMembers = map[string]member{
	"strux":    {true, exactly("anchor/1")},
	"log":      {true, isDigest},
	"key":      {true, isKey},
	"position": {true, isPosition},
	"head":     {true, isDigest},
	"observed": {true, isTime},
	"previous": {false, isDigest},
}

var refMembers = map[string]member{
	"fact":   {true, isDigest},
	"digest": {true, isDigest},
}

func checkFact(p map[string]any) error { return closed("the fact", p, factMembers) }

func closed(what string, obj map[string]any, members map[string]member) error {
	for name := range obj {
		if _, ok := members[name]; !ok {
			return fmt.Errorf("%s has member %q, which the encoding does not define", what, name)
		}
	}
	for _, name := range sortedKeys(members) {
		v, set := obj[name]
		if !set {
			if members[name].required {
				return fmt.Errorf("%s lacks member %q", what, name)
			}
			continue
		}
		if err := members[name].check(v); err != nil {
			return fmt.Errorf("%s: %s: %w", what, name, err)
		}
	}
	return nil
}

func exactly(want string) func(any) error {
	return func(v any) error {
		if v != want {
			return fmt.Errorf("is not %q", want)
		}
		return nil
	}
}

func isDigest(v any) error {
	if s, ok := v.(string); !ok || !digestSpelling.MatchString(s) {
		return errors.New("is not sha256: and 64 lowercase hexadecimal digits")
	}
	return nil
}

func isKey(v any) error {
	if s, ok := v.(string); !ok || !keySpelling.MatchString(s) {
		return errors.New("is not ed25519: and 64 lowercase hexadecimal digits")
	}
	return nil
}

func isString(v any) error {
	if s, ok := v.(string); !ok || s == "" || len(s) > DefaultStringMax {
		return fmt.Errorf("is not a string of 1 to %d bytes", DefaultStringMax)
	}
	return nil
}

func isTime(v any) error {
	s, ok := v.(string)
	if !ok {
		return errors.New("is not a time")
	}
	_, err := ParseTime(s)
	return err
}

func isPosition(v any) error {
	if n, ok := v.(int64); !ok || n < 0 {
		return errors.New("is not a position, an integer from 0")
	}
	return nil
}

func isRef(v any) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return errors.New(`is not {"fact": an identity, "digest": a content digest}`)
	}
	return closed("the ref", obj, refMembers)
}

func isDigests(v any) error {
	list, ok := v.([]any)
	if !ok || len(list) > DefaultListMax {
		return fmt.Errorf("is not a list of at most %d digests", DefaultListMax)
	}
	for _, d := range list {
		if err := isDigest(d); err != nil {
			return err
		}
	}
	return nil
}

func isCaptures(v any) error {
	if err := isDigests(v); err != nil {
		return err
	}
	list := v.([]any)
	if len(list) == 0 {
		return errors.New("is empty; omit it instead")
	}
	for i := 1; i < len(list); i++ {
		if list[i].(string) <= list[i-1].(string) {
			return errors.New("is not sorted, each digest once")
		}
	}
	return nil
}

// ReadAnchor reads an anchor's bytes, {"anchor": A, "sig": S}, checks A and that S signs the JCS
// form of A under A's key, and returns A's identity. An error means the anchor is not recognized.
func ReadAnchor(b []byte) (string, map[string]any, error) {
	v, err := ReadCanonical(b)
	if err != nil {
		return "", nil, fmt.Errorf("the anchor: %w", err)
	}
	obj, ok := v.(map[string]any)
	a, isObj := obj["anchor"].(map[string]any)
	sig, isSig := obj["sig"].(string)
	if !ok || len(obj) != 2 || !isObj || !isSig {
		return "", nil, errors.New(`the anchor is not {"anchor": A, "sig": S}`)
	}
	if err := closed("the anchor", a, anchorMembers); err != nil {
		return "", nil, err
	}
	if err := VerifySignature(a["key"].(string), Canonical(a), sig); err != nil {
		return "", nil, err
	}
	return Digest(a), a, nil
}
