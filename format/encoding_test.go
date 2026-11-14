package format

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"filippo.io/edwards25519"
)

// bom is the byte-order mark, which no JSON text may start with.
var bom = string(rune(0xFEFF))

// escaped writes a JSON escape of a code unit, so the tests spell escapes without writing them.
func escaped(unit uint16) string {
	return fmt.Sprintf(`\`+"u%04x", unit)
}

func TestParseJSONRefusesWhatLiesOutsideTheDataModel(t *testing.T) {
	for name, input := range map[string]string{
		"null":                    `{"a":null}`,
		"fraction":                `1.0`,
		"exponent":                `1e0`,
		"too large":               `9007199254740992`,
		"too small":               `-9007199254740992`,
		"leading zero":            `01`,
		"duplicate member":        `{"a":1,"a":2}`,
		"escaped duplicate":       `{"a":1,"` + escaped('a') + `":2}`,
		"lone high surrogate":     `"` + escaped(0xD800) + `"`,
		"lone low surrogate":      `"` + escaped(0xDC00) + `"`,
		"high then no low":        `"` + escaped(0xD800) + escaped('a') + `"`,
		"escaped noncharacter":    `"` + escaped(0xFFFF) + `"`,
		"raw noncharacter":        `"` + string(rune(0xFDD0)) + `"`,
		"byte-order mark":         bom + `{}`,
		"invalid UTF-8":           "\"\xff\"",
		"raw control character":   "\"\x01\"",
		"unknown escape":          `"\x"`,
		"string not closed":       `"abc`,
		"text after the value":    `{} {}`,
		"nothing":                 ``,
		"single quotes":           `'a'`,
		"nested beyond the bound": strings.Repeat("[", maxJSONDepth+2) + strings.Repeat("]", maxJSONDepth+2),
	} {
		if v, err := ParseJSON([]byte(input)); err == nil {
			t.Errorf("%s: accepted %q as %v", name, input, v)
		}
	}
	v, err := ParseJSON([]byte(" {\"b\" : [true, -9007199254740991, \"" + escaped(0xD83D) + escaped(0xDE00) + "\"], \"a\": {}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(Canonical(v)), `{"a":{},"b":[true,-9007199254740991,"`+string(rune(0x1F600))+`"]}`; got != want {
		t.Errorf("Canonical = %q, want %q", got, want)
	}
}

func TestReadCanonicalTakesOnlyTheJCSForm(t *testing.T) {
	for _, input := range []string{`{"a":1,"b":[true,"x"]}`, `"<>&/` + string(rune(0x2028)) + string(rune(0x7F)) + `"`, `"` + escaped(0x1F) + `"`, `0`, `[]`} {
		if _, err := ReadCanonical([]byte(input)); err != nil {
			t.Errorf("refused %q: %v", input, err)
		}
	}
	for _, input := range []string{`{"b":1,"a":2}`, `{"a": 1}`, `"\/"`, `"` + escaped(0xE9) + `"`, `-0`, "{\"a\":1}\n", `"` + escaped(0x41) + `"`} {
		if _, err := ReadCanonical([]byte(input)); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}

func TestParseTimeTakesOneSpelling(t *testing.T) {
	for _, s := range []string{"2026-10-30T12:00:00Z", "2026-10-30T12:00:00.1Z", "0001-01-01T00:00:00Z", "9999-12-31T23:59:59.999999999Z", "2024-02-29T00:00:00Z"} {
		if _, err := ParseTime(s); err != nil {
			t.Errorf("refused %s: %v", s, err)
		}
	}
	for _, s := range []string{
		"2026-10-30T12:00:00.100Z", "2026-10-30T12:00:00.000Z", "2026-10-30T12:00:00.Z", "2026-10-30T12:00:00+00:00",
		"2026-10-30T12:00:00z", "2026-10-30t12:00:00Z", "2026-12-31T23:59:60Z", "0000-01-01T00:00:00Z",
		"2026-10-30T12:00:00.1234567891Z", "2026-02-30T00:00:00Z", "2026-10-30 12:00:00Z", "2026-10-30T24:00:00Z", "26-10-30T12:00:00Z",
	} {
		if _, err := ParseTime(s); err == nil {
			t.Errorf("accepted %s", s)
		}
	}
	a, _ := ParseTime("2026-10-30T12:00:00Z")
	b, _ := ParseTime("2026-10-30T12:00:00.5Z")
	if !a.Before(b) {
		t.Errorf("times do not compare as instants")
	}
}

// A time an instance holds is read only in its one spelling, so an expression that reads another
// spelling cannot read it.
func TestFieldValueReadsTimesInTheirOneSpelling(t *testing.T) {
	f := &Field{Name: "when", Type: &Type{Kind: "scalar", Name: "time"}, Enabled: true}
	if _, err := fieldValue(map[string]any{"when": "2026-10-30T12:00:00.5Z"}, f); err != nil {
		t.Errorf("refused a time in its spelling: %v", err)
	}
	if _, err := fieldValue(map[string]any{"when": "2026-10-30T12:00:00.500Z"}, f); err == nil {
		t.Errorf("read a time with trailing zeros")
	}
}

// keyFor is the key of a seed, as the encoding spells it.
func keyFor(seed string) (ed25519.PrivateKey, string) {
	s := sha256.Sum256([]byte(seed))
	priv := ed25519.NewKeyFromSeed(s[:])
	return priv, "ed25519:" + hex.EncodeToString(priv.Public().(ed25519.PublicKey))
}

// torsion is a point of order 8.
func torsion(t *testing.T) *edwards25519.Point {
	t.Helper()
	b, _ := hex.DecodeString("c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a")
	p, err := new(edwards25519.Point).SetBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	eight := new(edwards25519.Point).MultByCofactor(p)
	four := new(edwards25519.Point).Add(p, p)
	four.Add(four, four)
	if eight.Equal(edwards25519.NewIdentityPoint()) != 1 || four.Equal(edwards25519.NewIdentityPoint()) == 1 {
		t.Fatal("the point is not of order 8")
	}
	return p
}

func TestValidKeyIsACanonicalPointOfPrimeOrder(t *testing.T) {
	_, key := keyFor("a key")
	if _, _, err := ValidKey(key); err != nil {
		t.Errorf("refused a key: %v", err)
	}
	base := "ed25519:" + hex.EncodeToString(edwards25519.NewGeneratorPoint().Bytes())
	if _, _, err := ValidKey(base); err != nil {
		t.Errorf("refused the base point: %v", err)
	}
	T := torsion(t)
	A, _, _ := ValidKey(key)
	mixed := new(edwards25519.Point).Add(A, T)
	for name, k := range map[string]string{
		"identity":         "ed25519:" + hex.EncodeToString(edwards25519.NewIdentityPoint().Bytes()),
		"small order":      "ed25519:" + hex.EncodeToString(T.Bytes()),
		"mixed order":      "ed25519:" + hex.EncodeToString(mixed.Bytes()),
		"y not reduced":    "ed25519:edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f",
		"x zero, sign set": "ed25519:0100000000000000000000000000000000000000000000000000000000000080",
		"upper case":       strings.ToUpper(key),
		"no prefix":        strings.TrimPrefix(key, "ed25519:"),
		"short":            key[:len(key)-2],
		"no point":         "ed25519:0200000000000000000000000000000000000000000000000000000000000000",
	} {
		if _, _, err := ValidKey(k); err == nil {
			t.Errorf("%s: accepted %s", name, k)
		}
	}
}

func TestVerifySignatureAsZIP215(t *testing.T) {
	priv, key := keyFor("a signer")
	msg := []byte(`{"a":1}`)
	sig := ed25519.Sign(priv, msg)
	if err := VerifySignature(key, msg, hex.EncodeToString(sig)); err != nil {
		t.Fatalf("refused a signature: %v", err)
	}
	if err := VerifySignature(key, []byte(`{"a":2}`), hex.EncodeToString(sig)); err == nil {
		t.Errorf("verified a signature over another message")
	}
	// S plus the group order: the equation would hold, and S is not reduced.
	ell, _ := new(big.Int).SetString("7237005577332262213973186563042994240857116359379907606001950938285454250989", 10)
	le := func(n *big.Int) []byte {
		be := n.FillBytes(make([]byte, 32))
		for i, j := 0, 31; i < j; i, j = i+1, j-1 {
			be[i], be[j] = be[j], be[i]
		}
		return be
	}
	sBig := new(big.Int).SetBytes(le(new(big.Int).SetBytes(sig[32:])))
	high := append(append([]byte{}, sig[:32]...), le(sBig.Add(sBig, ell))...)
	if err := VerifySignature(key, msg, hex.EncodeToString(high)); err == nil {
		t.Errorf("verified a signature whose S is not below the group order")
	}
	// R with a component of small order: only the cofactored equation holds.
	seed := sha256.Sum256([]byte("a signer"))
	h := sha512Sum(seed[:])
	a, _ := edwards25519.NewScalar().SetBytesWithClamping(h[:32])
	r, _ := edwards25519.NewScalar().SetUniformBytes(sha512Sum([]byte("a nonce")))
	R := new(edwards25519.Point).ScalarBaseMult(r)
	R.Add(R, torsion(t))
	pub := priv.Public().(ed25519.PublicKey)
	k := challenge(R.Bytes(), pub, msg)
	S := edwards25519.NewScalar().MultiplyAdd(k, a, r)
	cofactored := append(R.Bytes(), S.Bytes()...)
	if ed25519.Verify(pub, msg, cofactored) {
		t.Fatal("a cofactorless verifier accepts the signature, which then shows nothing")
	}
	if err := VerifySignature(key, msg, hex.EncodeToString(cofactored)); err != nil {
		t.Errorf("refused a signature the cofactored equation accepts: %v", err)
	}
	// R written with y = p, a point of order 4: any encoding of R is accepted.
	nonCanonical, _ := hex.DecodeString("edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f")
	k = challenge(nonCanonical, pub, msg)
	S = edwards25519.NewScalar().Multiply(k, a)
	if err := VerifySignature(key, msg, hex.EncodeToString(append(nonCanonical, S.Bytes()...))); err != nil {
		t.Errorf("refused a non-canonical R: %v", err)
	}
	// A small-order key: the equation holds and the key is no key.
	small := torsion(t).Bytes()
	n, _ := edwards25519.NewScalar().SetUniformBytes(sha512Sum([]byte("another nonce")))
	forged := append(new(edwards25519.Point).ScalarBaseMult(n).Bytes(), n.Bytes()...)
	if err := VerifySignature("ed25519:"+hex.EncodeToString(small), msg, hex.EncodeToString(forged)); err == nil {
		t.Errorf("verified a signature under a key of small order")
	}
}

// TestHeadIsTheMerkleTreeHash checks the head against the reference data of RFC 6962 that
// transparency-dev/merkle publishes.
func TestHeadIsTheMerkleTreeHash(t *testing.T) {
	var leaves [][]byte
	for _, l := range []string{"", "00", "10", "2021", "3031", "40414243", "5051525354555657", "606162636465666768696a6b6c6d6e6f"} {
		b, _ := hex.DecodeString(l)
		leaves = append(leaves, b)
	}
	for n, root := range map[int]string{
		1: "6e340b9cffb37a989ca544e6bb780a2c78901d3fb33738768511a30617afa01d",
		2: "fac54203e7cc696cf0dfcb42c92a1d9dbaf70ad9e621f4bd8d98662f00e3c125",
		3: "aeb6bcfe274b70a14fb067a5e5578264db0fa9b51af5e0ba159158f329e06e77",
		4: "d37ee418976dd95753c1c73862b9398fa2a2cf9b4ff0fdfe8b30cd95209614b7",
		5: "4e3bbb1f7b478dcfe71fb631631519a3bca12c9aefca1612bfce4c13a86264d4",
		6: "76e67dadbcdf1e10e1b74ddc608abd2f98dfb16fbce75277b5232a127f2087ef",
		7: "ddb89be403809e325750d3d263cd78929c2942b7942a34b77e122c9594a74c8c",
		8: "5dc9da79a70659a9ad559cb701ded9a2ab9d823aad2f4960cfe370eff4604328",
	} {
		if got := Head(leaves[:n]); got != "sha256:"+root {
			t.Errorf("head over %d leaves = %s, want %s", n, got, root)
		}
	}
}

func TestRecordsReadsOnlyEndedLines(t *testing.T) {
	for input, want := range map[string]int{"": 0, "a\n": 1, "a\nb\n": 2, "a\nb": 1, "a\nb\n{\"c\":": 2} {
		if got := Records([]byte(input)); len(got) != want {
			t.Errorf("Records(%q) reads %d records, want %d", input, len(got), want)
		}
	}
	if got := Records([]byte("a\nb\n")); string(got[1]) != "b" {
		t.Errorf("a record keeps its line feed: %q", got[1])
	}
}

// record signs a fact with each key and writes its record in the canonical form.
func record(p map[string]any, keys ...ed25519.PrivateKey) []byte {
	sigs := []any{}
	for _, k := range keys {
		sigs = append(sigs, map[string]any{
			"key": "ed25519:" + hex.EncodeToString(k.Public().(ed25519.PublicKey)),
			"sig": hex.EncodeToString(ed25519.Sign(k, Canonical(p))),
		})
	}
	return Canonical(map[string]any{"fact": p, "signatures": sigs})
}

// A refusal names the first thing the encoding refuses: the record, then the log it names, then a
// signature, then the content.
func TestARefusalNamesTheFirstThingRefused(t *testing.T) {
	agent, agentKey := keyFor("an agent")
	other, _ := keyFor("another agent")
	log, elsewhere := "sha256:"+strings.Repeat("ab", 32), "sha256:"+strings.Repeat("ef", 32)
	content, loose := []byte(`{"term":"anchor"}`), []byte(`{"term": "anchor"}`)
	digest := func(b []byte) string { sum := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(sum[:]) }
	fact := func(change func(map[string]any)) map[string]any {
		p := map[string]any{"strux": "fact/1", "log": log, "kind": "lexicon/glossary-entry", "package": "sha256:" + strings.Repeat("cd", 32), "content": digest(content), "key": agentKey}
		if change != nil {
			change(p)
		}
		return p
	}
	// forged is a record of p whose one signature was made over another fact.
	forged := func(p map[string]any) []byte {
		signed, err := ReadCanonical(record(fact(func(m map[string]any) { m["kind"] = "lexicon/another" }), agent))
		if err != nil {
			t.Fatal(err)
		}
		return Canonical(map[string]any{"fact": p, "signatures": signed.(map[string]any)["signatures"]})
	}
	p := fact(nil)
	spaced := fact(func(m map[string]any) { m["content"] = digest(loose) })
	away := fact(func(m map[string]any) { m["log"] = elsewhere })
	for name, tc := range map[string]struct {
		record, content []byte
		want            string
	}{
		"bytes out of the canonical form":       {append([]byte(" "), record(p, agent)...), content, RefusedRecord},
		"a time misspelt":                       {record(fact(func(m map[string]any) { m["append_by"] = "2026-11-30T00:00:00.000Z" }), agent), content, RefusedRecord},
		"two signatures for a fact with a key":  {record(p, agent, other), content, RefusedRecord},
		"a signature by another key than named": {record(p, other), content, RefusedRecord},
		"another log":                           {record(away, agent), content, RefusedLog},
		"a signature made over another fact":    {forged(p), content, RefusedSignature},
		"content that is not canonical":         {record(spaced, agent), loose, RefusedContent},
		"a misspelt time and another log":       {record(fact(func(m map[string]any) { m["log"], m["append_by"] = elsewhere, "noon" }), agent), content, RefusedRecord},
		"another log and a signature over none": {forged(away), content, RefusedLog},
		"a forged signature and loose content":  {forged(spaced), loose, RefusedSignature},
	} {
		_, err := ReadEntry(tc.record, tc.content, log)
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.What != tc.want {
			t.Errorf("%s: got %v, want a refusal of the %s", name, err, tc.want)
		}
	}
}

func TestReadEntry(t *testing.T) {
	agent, agentKey := keyFor("an agent")
	other, _ := keyFor("another agent")
	log := "sha256:" + strings.Repeat("ab", 32)
	content := []byte(`{"term":"anchor"}`)
	sum := sha256.Sum256(content)
	p := map[string]any{"strux": "fact/1", "log": log, "kind": "lexicon/glossary-entry", "package": "sha256:" + strings.Repeat("cd", 32), "content": "sha256:" + hex.EncodeToString(sum[:]), "key": agentKey}
	rec := record(p, agent)
	e, err := ReadEntry(rec, content, log)
	if err != nil {
		t.Fatalf("refused a valid entry: %v", err)
	}
	if e.Identity != Digest(p) || e.ContentAbsent {
		t.Errorf("identity %s, content absent %v; want %s and present", e.Identity, e.ContentAbsent, Digest(p))
	}
	if e, err := ReadEntry(rec, nil, log); err != nil || !e.ContentAbsent {
		t.Errorf("an entry without its content: %v, %v; want valid with its content absent", e, err)
	}
	if e, err := ReadEntry(rec, []byte(`{"term":"head"}`), log); err != nil || !e.ContentAbsent {
		t.Errorf("an entry with other content: %v, %v; want valid with its content absent", e, err)
	}
	loose := []byte(`{"term": "anchor"}`)
	looseSum := sha256.Sum256(loose)
	q := map[string]any{}
	for k, v := range p {
		q[k] = v
	}
	q["content"] = "sha256:" + hex.EncodeToString(looseSum[:])
	if _, err := ReadEntry(record(q, agent), loose, log); err == nil {
		t.Errorf("accepted content bytes that are not canonical, though their SHA-256 is the content digest")
	}
	if _, err := ReadEntry(rec, content, "sha256:"+strings.Repeat("ef", 32)); err == nil {
		t.Errorf("accepted a fact of another log")
	}
	if _, err := ReadEntry(record(p, agent, other), content, log); err == nil {
		t.Errorf("accepted a fact that names its key and carries two signatures")
	}
	if _, err := ReadEntry(record(p, other), content, log); err == nil {
		t.Errorf("accepted a fact that names one key and is signed by another")
	}
	if _, err := ReadEntry(append([]byte(" "), rec...), content, log); err == nil {
		t.Errorf("accepted a record that is not in the canonical form")
	}
	delete(q, "key")
	q["content"] = p["content"]
	if _, err := ReadEntry(record(q), content, log); err == nil {
		t.Errorf("accepted a record with no signature")
	}
	for name, change := range map[string]func(map[string]any){
		"captures empty":    func(m map[string]any) { m["captures"] = []any{} },
		"captures unsorted": func(m map[string]any) { m["captures"] = []any{log, "sha256:" + strings.Repeat("00", 32)} },
		"time misspelled":   func(m map[string]any) { m["append_by"] = "2026-11-30T00:00:00.000Z" },
		"unknown member":    func(m map[string]any) { m["note"] = "x" },
		"other version":     func(m map[string]any) { m["strux"] = "fact/2" },
		"ref open":          func(m map[string]any) { m["under"] = map[string]any{"fact": log, "digest": log, "instance": log} },
		"empty kind":        func(m map[string]any) { m["kind"] = "" },
	} {
		m := map[string]any{}
		for k, v := range p {
			m[k] = v
		}
		change(m)
		if _, err := ReadEntry(record(m, agent), content, log); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestReadAnchor(t *testing.T) {
	witness, key := keyFor("a witness")
	a := map[string]any{"strux": "anchor/1", "log": "sha256:" + strings.Repeat("ab", 32), "key": key, "position": int64(3), "head": "sha256:" + strings.Repeat("cd", 32), "observed": "2026-10-30T12:00:00.5Z"}
	signed := func(msg []byte) []byte {
		return Canonical(map[string]any{"anchor": a, "sig": hex.EncodeToString(ed25519.Sign(witness, msg))})
	}
	id, _, err := ReadAnchor(signed(Canonical(a)))
	if err != nil || id != Digest(a) {
		t.Fatalf("anchor: %s, %v; want %s", id, err, Digest(a))
	}
	if _, _, err := ReadAnchor(signed([]byte(Digest(a)))); err == nil {
		t.Errorf("accepted an anchor signed over its digest")
	}
	a["position"] = int64(-1)
	if _, _, err := ReadAnchor(signed(Canonical(a))); err == nil {
		t.Errorf("accepted a negative position")
	}
}

func sha512Sum(b []byte) []byte {
	h := sha512.New()
	h.Write(b)
	return h.Sum(nil)
}

func challenge(r, a, m []byte) *edwards25519.Scalar {
	h := sha512.New()
	h.Write(r)
	h.Write(a)
	h.Write(m)
	k, _ := edwards25519.NewScalar().SetUniformBytes(h.Sum(nil))
	return k
}
