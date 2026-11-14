package format

import (
	"fmt"
	"strings"

	"cel.dev/cel-go/common/types"
)

// Scalars are the built-in scalar types and their types in expressions.
var Scalars = map[string]*types.Type{
	"string":       types.StringType,
	"text":         types.StringType,
	"integer":      types.IntType,
	"boolean":      types.BoolType,
	"digest":       types.StringType,
	"time":         types.TimestampType,
	"position":     types.IntType,
	"name":         types.StringType,
	"name-pattern": types.StringType,
	"location":     types.StringType,
	"principal":    types.StringType,
}

// Default sizes, used where a field states no max.
const (
	DefaultStringMax = 1024
	DefaultTextMax   = 65536
	DefaultListMax   = 1000
)

// CombinationRules are the rules under which findings of a check combine.
var CombinationRules = []string{"unanimous", "all-satisfied", "majority"}

var constructors = map[string]bool{"list": true, "map": true, "enum": true, "ref": true, "any": true}

func builtin(name string, fields map[string]string) *Decl {
	d := &Decl{Package: builtinPackage, Name: name, Kind: "record", Builtin: true, Fields: map[string]*Field{}}
	for fname, src := range fields {
		d.Fields[fname] = &Field{Name: fname, TypeSrc: src, Enabled: true}
	}
	return d
}

// builtinRecords are the records every definition may name.
var builtinRecords = map[string]*Decl{
	"fact": builtin("fact", map[string]string{
		"kind": "string", "log": "digest", "package": "digest", "digest": "digest", "instance": "digest", "revision_digest": "digest",
		"expected": "digest", "position": "position", "signer": "principal", "credential": "string",
		"operation": "string", "operation_id": "string", "under": "ref(any)", "append_by": "time", "configuration": "list(digest)",
		"key": "string", "content": "digest", "captures": "list(digest)", "bootstrap": "boolean",
	}),
	"combination": builtin("combination", map[string]string{
		"rule": "enum(" + strings.Join(CombinationRules, ", ") + ")", "cap": "integer",
	}),
	"condition": builtin("condition", map[string]string{"rule": "text", "expr": "text"}),
}

// BuiltinTypeNames lists every built-in type a definition may name.
func BuiltinTypeNames() []string {
	var out []string
	for n := range Scalars {
		out = append(out, n)
	}
	for n := range builtinRecords {
		out = append(out, n)
	}
	return out
}

// BuiltinConstructorNames lists the built-in type constructors, such as ref, sorted.
func BuiltinConstructorNames() []string { return sortedKeys(constructors) }

// BuiltinRecordNames lists the built-in records, sorted.
func BuiltinRecordNames() []string { return sortedKeys(builtinRecords) }

// BuiltinFields lists the fields of a built-in record.
func BuiltinFields(name string) []string {
	if b := builtinRecords[name]; b != nil {
		return sortedKeys(b.Fields)
	}
	return nil
}

// Type is a parsed type expression.
type Type struct {
	Kind   string // scalar, list, map, enum, ref, named
	Name   string // a scalar or named type, as written
	Elem   *Type  // a list's elements, a map's values
	Key    *Type  // a map's keys
	Values []string
	Refs   []string // a reference's definitions, as written; "any" admits every definition
	Decl   *Decl    // what a named type resolved to
	Target []*Decl  // the definitions a reference admits; empty for any
}

// Resolved reports whether every name in the type resolved to a declaration.
func (t *Type) Resolved() bool {
	switch {
	case t == nil:
		return false
	case t.Kind == "named":
		return t.Decl != nil
	case t.Kind == "list" || t.Kind == "map":
		return t.Elem.Resolved()
	case t.Kind == "ref":
		named := 0
		for _, r := range t.Refs {
			if r != "any" {
				named++
			}
		}
		return len(t.Target) == named
	}
	return true
}

// ParseType parses a type expression such as list(ref(review-report)).
func ParseType(s string) (*Type, error) {
	p := &typeParser{s: s}
	t, err := p.typ()
	if err == nil {
		p.ws()
		if p.i != len(p.s) {
			err = fmt.Errorf("unexpected %q", p.s[p.i:])
		}
	}
	if err != nil {
		return nil, fmt.Errorf("type %q: %w", s, err)
	}
	return t, nil
}

type typeParser struct {
	s string
	i int
}

func (p *typeParser) ws() {
	for p.i < len(p.s) && p.s[p.i] == ' ' {
		p.i++
	}
}

func (p *typeParser) word() string {
	p.ws()
	start := p.i
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '/' || c == '.' || c == '_' {
			p.i++
			continue
		}
		break
	}
	return p.s[start:p.i]
}

func (p *typeParser) accept(c byte) bool {
	p.ws()
	if p.i < len(p.s) && p.s[p.i] == c {
		p.i++
		return true
	}
	return false
}

func (p *typeParser) words(sep byte) ([]string, error) {
	var out []string
	for {
		w := p.word()
		if w == "" {
			return nil, fmt.Errorf("expected a name at %d", p.i)
		}
		out = append(out, w)
		if !p.accept(sep) {
			break
		}
	}
	if !p.accept(')') {
		return nil, fmt.Errorf("expected ) at %d", p.i)
	}
	return out, nil
}

func (p *typeParser) typ() (*Type, error) {
	name := p.word()
	if name == "" {
		return nil, fmt.Errorf("expected a type at %d", p.i)
	}
	if !p.accept('(') {
		if Scalars[name] != nil {
			return &Type{Kind: "scalar", Name: name}, nil
		}
		if name == "any" {
			return nil, fmt.Errorf("any admits every definition only as ref(any)")
		}
		if constructors[name] {
			return nil, fmt.Errorf("%s needs arguments", name)
		}
		return &Type{Kind: "named", Name: name}, nil
	}
	switch name {
	case "list":
		elem, err := p.typ()
		if err != nil {
			return nil, err
		}
		if !p.accept(')') {
			return nil, fmt.Errorf("expected ) at %d", p.i)
		}
		return &Type{Kind: "list", Elem: elem}, nil
	case "map":
		key, err := p.typ()
		if err != nil {
			return nil, err
		}
		if !p.accept(',') {
			return nil, fmt.Errorf("map needs a key type and a value type")
		}
		val, err := p.typ()
		if err != nil {
			return nil, err
		}
		if !p.accept(')') {
			return nil, fmt.Errorf("expected ) at %d", p.i)
		}
		if (key.Kind != "scalar" || key.Name != "string") && key.Kind != "enum" {
			return nil, fmt.Errorf("a map key is string or an enum")
		}
		return &Type{Kind: "map", Key: key, Elem: val}, nil
	case "enum":
		vals, err := p.words(',')
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, v := range vals {
			if !enumValue.MatchString(v) {
				return nil, fmt.Errorf("%q is not an enum value", v)
			}
			if seen[v] {
				return nil, fmt.Errorf("%s is listed twice", v)
			}
			seen[v] = true
		}
		return &Type{Kind: "enum", Values: vals}, nil
	case "ref":
		refs, err := p.words('|')
		if err != nil {
			return nil, err
		}
		if len(refs) > 1 && contains(refs, "any") {
			return nil, fmt.Errorf("ref(any) admits every definition already")
		}
		for i, r := range refs {
			if contains(refs[:i], r) {
				return nil, fmt.Errorf("%s is listed twice", r)
			}
		}
		return &Type{Kind: "ref", Refs: refs}, nil
	}
	return nil, fmt.Errorf("%s takes no arguments", name)
}

// String writes the type back in definition syntax.
func (t *Type) String() string {
	switch t.Kind {
	case "list":
		return "list(" + t.Elem.String() + ")"
	case "map":
		return "map(" + t.Key.String() + ", " + t.Elem.String() + ")"
	case "enum":
		return "enum(" + strings.Join(t.Values, ", ") + ")"
	case "ref":
		return "ref(" + strings.Join(t.Refs, " | ") + ")"
	}
	return t.Name
}

// EnumValues returns the values a type admits, if it is an enumeration.
func (t *Type) EnumValues() []string {
	switch {
	case t.Kind == "enum":
		return t.Values
	case t.Kind == "named" && t.Decl != nil && t.Decl.Kind == "enum":
		return t.Decl.Values
	}
	return nil
}
