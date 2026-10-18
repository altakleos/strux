package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	yaml11 "go.yaml.in/yaml/v2"
	"go.yaml.in/yaml/v3"
)

func run(args ...string) (code int, stdout, stderr string) {
	var out, errs bytes.Buffer
	code = Run(context.Background(), args, &out, &errs)
	return code, out.String(), errs.String()
}

// The exit codes are written out, not taken from the constants, because the numbers are what
// strux promises.
func TestExitCodes(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		code   int
		stderr string
	}{
		{nil, 0, ""},
		{[]string{"help"}, 0, ""},
		{[]string{"--help"}, 0, ""},
		{[]string{"help", "version"}, 0, ""},
		{[]string{"version", "-h"}, 0, ""},
		{[]string{"help", "-h"}, 0, ""},
		{[]string{"version"}, 0, ""},
		{[]string{"--version"}, 0, ""},
		{[]string{"version", "--json"}, 0, ""},
		{[]string{"verison"}, 2, `strux: unknown command "verison"; did you mean "version"? (see "strux help")`}, //nolint:misspell // a typo strux corrects
		{[]string{"frobnicate"}, 2, `strux: unknown command "frobnicate" (see "strux help")`},
		{[]string{"frobnicate", "--help"}, 2, `strux: unknown command "frobnicate" (see "strux help")`},
		{[]string{"-h", "frobnicate"}, 2, `strux: unknown command "frobnicate" (see "strux help")`},
		{[]string{""}, 2, `strux: unknown command "" (see "strux help")`},
		{[]string{"--", "version"}, 2, `strux: command "version" must come before "--" (see "strux help")`},
		{[]string{"help", "frobnicate"}, 2, `strux: unknown command "frobnicate" (see "strux help")`},
		{[]string{"help", "version", "extra"}, 2, `strux: unknown command "extra" for "strux version" (see "strux help version")`},
		{[]string{"version", "extra"}, 2, `strux: strux version takes no arguments (see "strux help version")`},
		{[]string{"version", "extra", "--help"}, 2, `strux: strux version takes no arguments (see "strux help version")`},
		{[]string{"version", "--frob"}, 2, `strux: unknown flag: --frob (see "strux help version")`},
		{[]string{"--version", "extra"}, 2, `strux: unknown command "extra" (see "strux help")`},
		{[]string{"__complete", "ver"}, 2, `strux: unknown command "__complete" (see "strux help")`},
		{[]string{"__complete"}, 2, `strux: unknown command "__complete" (see "strux help")`},
		{[]string{"--json", "__completeNoDesc", "ver"}, 2, `strux: unknown command "__completeNoDesc" (see "strux help")`},
	} {
		code, stdout, stderr := run(tc.args...)
		if code != tc.code {
			t.Errorf("strux %q: exit code %d, want %d", tc.args, code, tc.code)
		}
		if got := strings.TrimSuffix(stderr, "\n"); got != tc.stderr {
			t.Errorf("strux %q: stderr %q, want %q", tc.args, got, tc.stderr)
		}
		if (code == 0) != (stdout != "") {
			t.Errorf("strux %q: exit code %d with stdout %q", tc.args, code, stdout)
		}
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

// A result that cannot be written is a failure, whichever way it is asked for.
func TestWriteFailureExitsOne(t *testing.T) {
	for _, args := range [][]string{nil, {"version"}, {"version", "--json"}, {"--help"}, {"help", "version"}} {
		var errs bytes.Buffer
		if code := Run(context.Background(), args, failingWriter{}, &errs); code != 1 {
			t.Errorf("strux %q: exit code %d, want 1", args, code)
		}
		if got, want := errs.String(), "strux: disk full\n"; got != want {
			t.Errorf("strux %q: stderr %q, want %q", args, got, want)
		}
	}
}

// Every result's YAML and JSON forms decode to the same value.
func TestFormsCarryTheSameFields(t *testing.T) {
	for _, args := range [][]string{nil, {"help", "version"}, {"version"}} {
		_, text, _ := run(args...)
		_, js, _ := run(append(args, "--json")...)
		var fromText, fromJSON any
		if err := yaml.Unmarshal([]byte(text), &fromText); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(js), &fromJSON); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(fromText, fromJSON) {
			t.Errorf("strux %q: YAML and JSON differ:\n%s\n%s", args, text, js)
		}
	}
}

// A YAML 1.1 reader and a YAML 1.2 reader both read a result's YAML as the value its JSON holds:
// the same strings, numbers, booleans and nulls, whatever characters the strings contain.
func TestYAMLReadsAsTheJSON(t *testing.T) {
	strs := []string{
		"v0.1.0", "go1.27.2", "darwin/arm64", "Show this build's version.", "strux [command] [flags]",
		"(devel)", "--json", "", " leading", "trailing ", "two\nlines", "tab\there", "cr\rhere",
		// YAML 1.1 reads these as booleans or null when they are not quoted.
		"y", "Y", "n", "N", "yes", "Yes", "YES", "no", "No", "NO", "on", "On", "ON", "off", "Off", "OFF",
		"true", "True", "TRUE", "false", "False", "FALSE", "null", "Null", "NULL", "~",
		// Numbers, dates and other types.
		"0", "-1", "0123", "0x1f", "0o17", "1e3", "1_000", "12:30", ".inf", "-.inf", ".nan", "2026-10-18",
		"2026-10-18T09:00:00Z", "1.5",
		// Indicators and structure.
		"a: b", "a #b", "#a", `"quoted"`, "'single'", "@at", "`tick`", "!tag", "&anchor", "*alias",
		"%directive", "|", ">", "-", "- item", "? key", "{a}", "[a]", ",", "a,b", "<<",
		// Characters a YAML reader would refuse or change if they were written as they are.
		"nul\x00", "bell\a", "esc\x1b", "del\x7f", "c1\u0080\u0085\u009f", "nel\u0085nel", "bom\ufeff",
		"nonchar\ufffe\uffff", "ls\u2028ps\u2029", "nbsp\u00a0", "emoji \U0001F600", "\u00e9", "\u65e5\u672c\u8a9e",
	}
	value := map[string]any{
		"strings": strs,
		"numbers": []any{0, -3, 42, 9007199254740993, 1.5, -0.25, 1e21, 1e-7, 1.5e30, -2e-300},
		"others":  []any{true, false, nil, map[string]any{}, []any{}},
		"keys":    map[string]int{"yes": 1, "a: b": 2, "": 3, strings.Repeat("k", 2000): 4, "nel\u0085": 5},
	}
	var text, js bytes.Buffer
	if err := (&output{w: &text}).write(value); err != nil {
		t.Fatal(err)
	}
	if err := (&output{w: &js, json: true}).write(value); err != nil {
		t.Fatal(err)
	}
	var want, got12, got11 any
	dec := json.NewDecoder(&js)
	dec.UseNumber()
	if err := dec.Decode(&want); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(text.Bytes(), &got12); err != nil {
		t.Fatalf("YAML 1.2: %v\n%s", err, text.String())
	}
	if err := yaml11.Unmarshal(text.Bytes(), &got11); err != nil {
		t.Fatalf("YAML 1.1: %v\n%s", err, text.String())
	}
	want = normalize(want)
	for reader, got := range map[string]any{"YAML 1.2": got12, "YAML 1.1": got11} {
		if got := normalize(got); !reflect.DeepEqual(got, want) {
			t.Errorf("%s reads the YAML as\n%#v\nbut the JSON holds\n%#v\n%s", reader, got, want, text.String())
		}
	}
}

// normalize gives a decoded value one representation whichever decoder produced it: maps keyed
// by string, and numbers as their float64 values.
func normalize(v any) any {
	switch v := v.(type) {
	case map[string]any:
		m := map[string]any{}
		for k, e := range v {
			m[k] = normalize(e)
		}
		return m
	case map[any]any:
		m := map[string]any{}
		for k, e := range v {
			m[fmt.Sprint(k)] = normalize(e)
		}
		return m
	case []any:
		s := make([]any, len(v))
		for i, e := range v {
			s[i] = normalize(e)
		}
		return s
	case json.Number:
		f, _ := v.Float64()
		return f
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case uint64:
		return float64(v)
	}
	return v
}

// The YAML 1.1 and YAML 1.2 core schema patterns for decimal numbers, from yaml.org/type and the
// YAML 1.2 specification. go.yaml.in/yaml/v2 reads more forms as numbers than YAML 1.1 does, so
// it cannot check them.
var (
	int11   = regexp.MustCompile(`^[-+]?(0|[1-9][0-9_]*)$`)
	float11 = regexp.MustCompile(`^[-+]?([0-9][0-9_]*)?\.[0-9.]*([eE][-+][0-9]+)?$`)
	int12   = regexp.MustCompile(`^[-+]?[0-9]+$`)
	float12 = regexp.MustCompile(`^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$`)
)

// Every JSON number is written in a form that both YAML versions read as that number.
func TestNumbersAreNumbersInEveryYAML(t *testing.T) {
	for _, js := range []string{"0", "-3", "42", "1.5", "-0.25", "1e+21", "1e-7", "1.5e+30", "-2e-300", "1E5", "2.5e10"} {
		n := number(js)
		want, _ := strconv.ParseFloat(js, 64)
		if got, err := strconv.ParseFloat(n.Value, 64); err != nil || got != want {
			t.Errorf("%s is written as %s, which is %v", js, n.Value, got)
		}
		switch n.Tag {
		case "!!int":
			if !int11.MatchString(n.Value) || !int12.MatchString(n.Value) {
				t.Errorf("%s is written as %s, which is not an integer in YAML 1.1 and 1.2", js, n.Value)
			}
		case "!!float":
			if !float11.MatchString(n.Value) || !float12.MatchString(n.Value) {
				t.Errorf("%s is written as %s, which is not a float in YAML 1.1 and 1.2", js, n.Value)
			}
		default:
			t.Errorf("%s is written with tag %s", js, n.Tag)
		}
	}
}
