package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
)

// output writes command results to stdout.
type output struct {
	w    io.Writer
	json bool  // --json
	err  error // what went wrong answering --help, which cobra gives no way to return
}

// runs returns a cobra RunE that computes a command's result with f and writes it.
func (o *output) runs(f func(*cobra.Command, []string) (any, error)) func(*cobra.Command, []string) error {
	return func(c *cobra.Command, args []string) error {
		v, err := f(c, args)
		if err != nil {
			return err
		}
		return o.write(v)
	}
}

// write writes a result as JSON with --json, and otherwise as YAML built from the tokens of that
// JSON, so both forms carry the same values in the same order.
func (o *output) write(v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	if !o.json {
		dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
		dec.UseNumber()
		n, err := node(dec)
		if err != nil {
			return err
		}
		buf.Reset()
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{n}}); err != nil {
			return err
		}
		if err := enc.Close(); err != nil {
			return err
		}
	}
	_, err := o.w.Write(buf.Bytes())
	return err
}

// node reads the next JSON value from dec as a YAML node.
func node(dec *json.Decoder) (*yaml.Node, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		if t == '{' {
			n.Kind, n.Tag = yaml.MappingNode, "!!map"
		}
		for dec.More() {
			if n.Kind == yaml.MappingNode {
				key, err := dec.Token()
				if err != nil {
					return nil, err
				}
				n.Content = append(n.Content, text(key.(string)))
			}
			v, err := node(dec)
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, v)
		}
		_, err := dec.Token() // the closing delimiter
		return n, err
	case string:
		return text(t), nil
	case json.Number:
		return number(t.String()), nil
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(t)}, nil
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	}
	return nil, fmt.Errorf("unexpected JSON token %v", tok)
}

// text is a string, quoted unless it is plain-safe. The YAML encoder escapes every character a
// quoted string cannot hold as it is.
func text(s string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
	if !plainSafe(s) {
		n.Style = yaml.DoubleQuotedStyle
	}
	return n
}

// number is a JSON number in a form that a YAML 1.1 reader and a YAML 1.2 reader both read as
// that number. YAML 1.1 reads an exponent as part of a number only after a decimal point, and
// only with a sign: 1e+21 is a string to it, and 1.0e+21 a float.
func number(s string) *yaml.Node {
	tag := "!!int"
	if mantissa, exp, ok := strings.Cut(strings.ToLower(s), "e"); ok {
		if !strings.Contains(mantissa, ".") {
			mantissa += ".0"
		}
		if exp[0] != '+' && exp[0] != '-' {
			exp = "+" + exp
		}
		s, tag = mantissa+"e"+exp, "!!float"
	} else if strings.Contains(s, ".") {
		tag = "!!float"
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: s}
}

var plainText = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 ._/+',()<>\[\]-]*$`)

// plainSafe reports whether s can be written as a plain YAML scalar that a YAML 1.1 reader and a
// YAML 1.2 reader both read back as the string s. It must start with a letter, and contain only
// letters, digits, spaces and punctuation that cannot start a comment, a mapping or a different
// type. It must not end in a space or be one of YAML 1.1's boolean or null words.
func plainSafe(s string) bool {
	if !plainText.MatchString(s) || strings.HasSuffix(s, " ") {
		return false
	}
	switch strings.ToLower(s) {
	case "y", "n", "yes", "no", "on", "off", "true", "false", "null":
		return false
	}
	return true
}
