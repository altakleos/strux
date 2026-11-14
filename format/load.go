// Package format implements definition format 1 and interpretation revision 1: strict YAML,
// canonical JSON identity, typed structure, and CEL conditions.
package format

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"go.yaml.in/yaml/v3"
)

const maxInteger = 1<<53 - 1

var (
	decimal = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)
	digits  = regexp.MustCompile(`^[-+]?[0-9]+$`)
	// yaml11Bool lists the plain scalars YAML 1.1 reads as booleans and YAML 1.2 as strings.
	yaml11Bool = map[string]bool{
		"y": true, "Y": true, "yes": true, "Yes": true, "YES": true, "n": true, "N": true, "no": true, "No": true, "NO": true,
		"on": true, "On": true, "ON": true, "off": true, "Off": true, "OFF": true,
	}
)

// Parse reads one YAML document in the strict subset and returns its content in the JSON data
// model: map[string]any, []any, string, int64 and bool.
func Parse(data []byte) (any, error) {
	if strings.TrimSpace(string(data)) == "" {
		return nil, fmt.Errorf("the file is empty")
	}
	for i, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "%") && strings.TrimSpace(line) != "%YAML 1.2" {
			return nil, fmt.Errorf("line %d: the only directive allowed is %%YAML 1.2", i+1)
		}
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc, extra yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil, syntaxError(err)
	}
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) || doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, fmt.Errorf("expected exactly one YAML document")
	}
	r := &reader{lines: strings.Split(string(data), "\n")}
	return r.value(doc.Content[0])
}

var (
	// parserProblem matches what yaml.v3's parser, as opposed to its scanner, reports: the
	// problems its parser sets. It names a parser error's line counting from 0, and omits line 0.
	parserProblem = regexp.MustCompile(`^yaml: (?:line (\d+): )?((?:did not find expected|found undefined tag handle|found duplicate %YAML directive|found incompatible YAML document|found duplicate %TAG directive).*)$`)
	// scannerProblem matches a scanner error, whose line yaml.v3 counts from 1.
	scannerProblem = regexp.MustCompile(`^yaml: line (\d+): (.*)$`)
)

// syntaxError restates a yaml.v3 syntax error as Parse states every error: at the line it names,
// counting from 1. A parser error names the line of the construct it was parsing.
func syntaxError(err error) error {
	msg := err.Error()
	if m := parserProblem.FindStringSubmatch(msg); m != nil {
		n, _ := strconv.Atoi(m[1]) // 0 where yaml.v3 omits the line
		return fmt.Errorf("line %d: %s", n+1, m[2])
	}
	if m := scannerProblem.FindStringSubmatch(msg); m != nil {
		return fmt.Errorf("line %s: %s", m[1], m[2])
	}
	return fmt.Errorf("%s", strings.TrimPrefix(msg, "yaml: "))
}

// reader converts nodes, with the source they came from: yaml.v3 drops the non-specific tag !
// from nodes, but a node's position still points at it.
type reader struct{ lines []string }

// marked refuses what may mark any node, key or value: anchors and explicit tags.
func (r *reader) marked(n *yaml.Node) error {
	tagged := n.Style&yaml.TaggedStyle != 0
	if n.Line >= 1 && n.Line <= len(r.lines) {
		if line := []rune(r.lines[n.Line-1]); n.Column >= 1 && n.Column <= len(line) && line[n.Column-1] == '!' {
			tagged = true
		}
	}
	switch {
	case n.Anchor != "":
		return fmt.Errorf("line %d: anchors are not allowed", n.Line)
	case tagged:
		return fmt.Errorf("line %d: explicit tags are not allowed", n.Line)
	}
	return nil
}

func (r *reader) value(n *yaml.Node) (any, error) {
	at := func(format string, args ...any) error {
		return fmt.Errorf("line %d: %s", n.Line, fmt.Sprintf(format, args...))
	}
	if err := r.marked(n); err != nil {
		return nil, err
	}
	switch n.Kind {
	case yaml.AliasNode:
		return nil, at("aliases are not allowed")
	case yaml.MappingNode:
		out := make(map[string]any, len(n.Content)/2)
		for i := 0; i < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if err := r.marked(k); err != nil {
				return nil, err
			}
			switch {
			case k.Kind == yaml.ScalarNode && k.ShortTag() == "!!merge":
				return nil, fmt.Errorf("line %d: merge keys are not allowed", k.Line)
			case k.Kind != yaml.ScalarNode || k.ShortTag() != "!!str":
				return nil, fmt.Errorf("line %d: a key must be a string; quote it if it reads as another kind of value", k.Line)
			}
			if err := ambiguous(k); err != nil {
				return nil, err
			}
			if _, dup := out[k.Value]; dup {
				return nil, fmt.Errorf("line %d: duplicate key %q", k.Line, k.Value)
			}
			val, err := r.value(v)
			if err != nil {
				return nil, err
			}
			out[k.Value] = val
		}
		return out, nil
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			val, err := r.value(c)
			if err != nil {
				return nil, err
			}
			out = append(out, val)
		}
		return out, nil
	case yaml.ScalarNode:
		switch n.ShortTag() {
		case "!!str":
			if err := ambiguous(n); err != nil {
				return nil, err
			}
			return n.Value, nil
		case "!!bool":
			return n.Value == "true" || n.Value == "True" || n.Value == "TRUE", nil
		case "!!int":
			if !decimal.MatchString(n.Value) {
				return nil, at("%q reads differently under YAML 1.1 and 1.2: write integers in decimal, or quote the value", n.Value)
			}
			i, err := strconv.ParseInt(n.Value, 10, 64)
			if err != nil || i > maxInteger || i < -maxInteger {
				return nil, at("integer %s is out of range", n.Value)
			}
			return i, nil
		case "!!float":
			if digits.MatchString(n.Value) {
				return nil, at("integer %s is out of range", n.Value)
			}
			return nil, at("a float is not allowed; quote it to make it a string")
		case "!!null":
			return nil, at("null is not allowed; omit the key instead")
		default:
			return nil, at("a %s value is not allowed; quote it to make it a string", strings.TrimPrefix(n.ShortTag(), "!!"))
		}
	}
	return nil, at("unexpected YAML node")
}

// ambiguous refuses a plain scalar that YAML 1.1 reads as a boolean.
func ambiguous(n *yaml.Node) error {
	if n.Style == 0 && yaml11Bool[n.Value] {
		return fmt.Errorf("line %d: %q is a boolean under YAML 1.1 and a string under 1.2; quote it", n.Line, n.Value)
	}
	return nil
}

// Canonical returns the RFC 8785 (JSON Canonicalization Scheme) serialization of a value in
// the data model.
func Canonical(v any) []byte {
	var b strings.Builder
	canonical(&b, v)
	return []byte(b.String())
}

// Members returns the members of an object in the order of its canonical form, each as that form
// writes it: a name, a colon and a value. Joined by commas, in braces, they are that form.
func Members(object map[string]any) [][]byte {
	keys := make([]string, 0, len(object))
	for k := range object {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })
	members := make([][]byte, 0, len(keys))
	for _, k := range keys {
		var b strings.Builder
		quote(&b, k)
		b.WriteByte(':')
		canonical(&b, object[k])
		members = append(members, []byte(b.String()))
	}
	return members
}

func canonical(b *strings.Builder, v any) {
	switch x := v.(type) {
	case map[string]any:
		b.WriteByte('{')
		for i, member := range Members(x) {
			if i > 0 {
				b.WriteByte(',')
			}
			b.Write(member)
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			canonical(b, e)
		}
		b.WriteByte(']')
	case string:
		quote(b, x)
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case bool:
		b.WriteString(strconv.FormatBool(x))
	default:
		panic(fmt.Sprintf("value outside the data model: %T", v))
	}
}

// lessUTF16 orders strings by their UTF-16 code units, as RFC 8785 requires.
func lessUTF16(a, b string) bool {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

func quote(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// Digest returns sha256: and the hexadecimal SHA-256 of a value's canonical form.
func Digest(v any) string {
	sum := sha256.Sum256(Canonical(v))
	return "sha256:" + hex.EncodeToString(sum[:])
}
