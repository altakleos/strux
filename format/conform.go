package format

import (
	"fmt"
	"regexp"
	"strings"
)

// locationSpelling is the shape of a location: a line, or a range of lines.
var locationSpelling = regexp.MustCompile(`^L[1-9][0-9]*(-L[1-9][0-9]*)?$`)

// Conforms checks a value against a declared type, as the format's field specifications say an
// instance conforms to its structure: every required field set, no field the type lacks, each
// value of its field's type, no string, list or map above its size, and no two items of a list
// sharing its key. The value is held as it is parsed, and the type's names are resolved. It
// returns what fails, each with the path of the field, and nothing for a value that conforms.
func Conforms(decl *Decl, value any) []string {
	var c conformer
	c.decl(decl, value, "", false)
	return c.problems
}

// ConformsPartly is Conforms for a value that states only some of the type's fields at its top
// level: a required field it leaves out is no failure, and every field it states conforms in
// full.
func ConformsPartly(decl *Decl, value any) []string {
	var c conformer
	c.decl(decl, value, "", true)
	return c.problems
}

// Refer says what is wrong with a reference that a value holds, given the definitions its field
// admits, none for a field that admits any. It returns nothing for a reference it finds no fault
// with.
type Refer func(reference map[string]any, admits []*Decl) string

// ConformsTo is Conforms with a check of each reference the value holds. Conforms holds a
// reference to its form alone, since only a caller that knows the facts can say what one names.
func ConformsTo(decl *Decl, value any, refer Refer) []string {
	c := conformer{refer: refer}
	c.decl(decl, value, "", false)
	return c.problems
}

type conformer struct {
	problems []string
	refer    Refer // nil where references are held to their form alone
}

func (c *conformer) failf(where, format string, args ...any) {
	if where == "" {
		where = "the value"
	}
	c.problems = append(c.problems, where+": "+fmt.Sprintf(format, args...))
}

func join(where, field string) string {
	if where == "" {
		return field
	}
	return where + "." + field
}

// decl checks a value against a record, a variant, a definition's structure or an enumeration.
func (c *conformer) decl(d *Decl, v any, where string, partly bool) {
	if d == nil {
		c.failf(where, "its type did not resolve")
		return
	}
	if d.Kind == "enum" {
		c.enum(d.Values, v, where)
		return
	}
	m, ok := v.(map[string]any)
	if !ok {
		c.failf(where, "must be a mapping of the fields of %s", d.Path())
		return
	}
	for _, name := range sortedKeys(m) {
		f := d.Fields[name]
		if f == nil {
			c.failf(join(where, name), "%s has no such field", d.Path())
			continue
		}
		if f.Type == nil {
			c.failf(join(where, name), "its type did not resolve")
			continue
		}
		c.field(f, m[name], join(where, name))
	}
	switch {
	case d.Kind == "variant":
		if len(m) != 1 {
			c.failf(where, "a value of %s sets exactly one of its alternatives, and this sets %d", d.Path(), len(m))
		}
	case !partly:
		for _, name := range sortedKeys(d.Fields) {
			// The fields of a built-in record are all set, since the format gives none a default.
			if f := d.Fields[name]; (f.Required || d.Builtin) && m[name] == nil {
				c.failf(join(where, name), "is required")
			}
		}
	}
}

// field checks a value against a field specification: its type, and the size and key it states.
func (c *conformer) field(f *Field, v any, where string) {
	c.typ(f.Type, v, where, f.Max)
	if f.Key == "" {
		return
	}
	items, _ := v.([]any)
	seen := map[string]bool{}
	for i, item := range items {
		key, set := mapping(item)[f.Key]
		if !set {
			continue
		}
		k := fmt.Sprint(key)
		if seen[k] {
			c.failf(fmt.Sprintf("%s[%d]", where, i), "shares its %s, %s, with an earlier item", f.Key, k)
		}
		seen[k] = true
	}
}

// typ checks a value against a type. max is the size its field states, or zero for the default.
func (c *conformer) typ(t *Type, v any, where string, max int64) {
	switch t.Kind {
	case "scalar":
		c.scalar(t.Name, v, where, max)
	case "enum":
		c.enum(t.Values, v, where)
	case "list":
		items, ok := v.([]any)
		if !ok {
			c.failf(where, "must be a list")
			return
		}
		if max == 0 {
			max = DefaultListMax
		}
		if int64(len(items)) > max {
			c.failf(where, "holds %d items, more than %d", len(items), max)
		}
		for i, item := range items {
			c.typ(t.Elem, item, fmt.Sprintf("%s[%d]", where, i), 0)
		}
	case "map":
		m, ok := v.(map[string]any)
		if !ok {
			c.failf(where, "must be a mapping")
			return
		}
		if max == 0 {
			max = DefaultListMax
		}
		if int64(len(m)) > max {
			c.failf(where, "holds %d entries, more than %d", len(m), max)
		}
		for _, k := range sortedKeys(m) {
			c.typ(t.Key, k, where+"["+k+"]", 0)
			c.typ(t.Elem, m[k], where+"["+k+"]", 0)
		}
	case "ref":
		m, ok := v.(map[string]any)
		if !ok || len(m) != 2 || !isDigestValue(m["fact"]) || !isDigestValue(m["digest"]) {
			c.failf(where, "must be a reference: the identity of a fact and a content digest")
			return
		}
		if c.refer != nil {
			if problem := c.refer(m, t.Target); problem != "" {
				c.failf(where, "%s", problem)
			}
		}
	case "named":
		c.decl(t.Decl, v, where, false)
	default:
		c.failf(where, "has a type the checker does not know")
	}
}

func isDigestValue(v any) bool {
	s, ok := v.(string)
	return ok && digestSpelling.MatchString(s)
}

func (c *conformer) enum(values []string, v any, where string) {
	if s, ok := v.(string); !ok || !contains(values, s) {
		c.failf(where, "must be one of %s", strings.Join(values, ", "))
	}
}

func (c *conformer) scalar(name string, v any, where string, max int64) {
	switch name {
	case "integer", "position":
		n, ok := v.(int64)
		switch {
		case !ok || n > maxInteger || n < -maxInteger:
			c.failf(where, "must be an integer")
		case name == "position" && n < 0:
			c.failf(where, "a position is not negative")
		}
		return
	case "boolean":
		if _, ok := v.(bool); !ok {
			c.failf(where, "must be true or false")
		}
		return
	}
	s, ok := v.(string)
	if !ok {
		c.failf(where, "must be a %s", name)
		return
	}
	switch name {
	case "string", "text":
		if max == 0 {
			max = DefaultStringMax
			if name == "text" {
				max = DefaultTextMax
			}
		}
		switch {
		case s == "":
			c.failf(where, "a %s is never empty", name)
		case int64(len(s)) > max:
			c.failf(where, "is %d bytes, more than %d", len(s), max)
		}
	case "digest":
		if !digestSpelling.MatchString(s) {
			c.failf(where, "must be a digest: sha256: and 64 lowercase hexadecimal digits")
		}
	case "time":
		if _, err := ParseTime(s); err != nil {
			c.failf(where, "must be a time in the spelling the encoding fixes")
		}
	case "name", "name-pattern":
		if s == "" || strings.Contains("/"+s+"/", "//") {
			c.failf(where, "must be non-empty segments separated by /")
		}
	case "location":
		if !locationSpelling.MatchString(s) {
			c.failf(where, "must be a line or a line range, as L52 or L52-L60")
		}
	case "principal":
		if s == "" {
			c.failf(where, "a principal is never empty")
		}
	}
}
