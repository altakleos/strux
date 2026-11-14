package format

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadExample reads the example packages the test names from testdata/binder: a neutral package that
// declares an abstract operation, a package that binds it, and one that performs it.
func loadExample(t *testing.T, names ...string) map[string]*Package {
	t.Helper()
	out := map[string]*Package{}
	for _, n := range names {
		data, err := fs.ReadFile(os.DirFS(filepath.Join("testdata", "binder")), n+".yaml")
		if err != nil {
			t.Fatal(err)
		}
		c, err := Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		out[n] = Build(n, n+"/definitions.yaml", c)
	}
	return out
}

func with(t *testing.T, name, src string) *Package {
	t.Helper()
	c, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return Build(name, name+"/definitions.yaml", c)
}

func TestBinderExampleChecks(t *testing.T) {
	pkgs := loadExample(t, "art-neutral", "art-git", "art-performer")
	if errs := Check(pkgs); len(errs) > 0 {
		t.Fatalf("working example refused: %v", errs)
	}
	if errs := CheckBinders(pkgs, []string{"art-neutral", "art-git", "art-performer"}); len(errs) > 0 {
		t.Fatalf("a version listing the binder was refused: %v", errs)
	}
}

func TestVersionWithoutBinderIsRefused(t *testing.T) {
	pkgs := loadExample(t, "art-neutral", "art-git", "art-performer")
	errs := CheckBinders(pkgs, []string{"art-neutral", "art-performer"})
	if len(errs) != 1 || !strings.Contains(errs[0], "lists no package that binds it") {
		t.Fatalf("got %v", errs)
	}
}

func TestVersionWithTwoBindersIsRefused(t *testing.T) {
	pkgs := loadExample(t, "art-neutral", "art-git", "art-performer")
	pkgs["art-hg"] = with(t, "art-hg", strings.ReplaceAll(strings.ReplaceAll(sourceOf(t, "art-git"), "package: art-git", "package: art-hg"), "git-capture", "hg-capture"))
	errs := CheckBinders(pkgs, []string{"art-neutral", "art-git", "art-hg", "art-performer"})
	if len(errs) != 1 || !strings.Contains(errs[0], "lists 2 binders") {
		t.Fatalf("got %v", errs)
	}
	// Listing only one of the two is fine.
	if errs := CheckBinders(pkgs, []string{"art-neutral", "art-hg", "art-performer"}); len(errs) > 0 {
		t.Fatalf("one binder of two refused: %v", errs)
	}
}

func sourceOf(t *testing.T, n string) string {
	t.Helper()
	data, err := fs.ReadFile(os.DirFS(filepath.Join("testdata", "binder")), n+".yaml")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestBinderWithWrongInputsIsRefused(t *testing.T) {
	base := sourceOf(t, "art-git")
	for name, tc := range map[string]struct{ from, to, want string }{
		"missing input":  {"          operation_id: { type: string, required: true }\n", "", "takes no input operation_id"},
		"wrong type":     {"          operation_id: { type: string, required: true }", "          operation_id: { type: integer, required: true }", "has type integer"},
		"extra required": {"          commit: { type: string, meaning: \"The commit to read; the head of the default branch when absent.\" }", "          commit: { type: string, meaning: \"The commit to read; the head of the default branch when absent.\" }\n          extra: { type: string, required: true }", "input extra is required by the binder but not taken by"},
		"extra optional": {"          commit: { type: string, meaning: \"The commit to read; the head of the default branch when absent.\" }", "          commit: { type: string, meaning: \"The commit to read; the head of the default branch when absent.\" }\n          extra: { type: string }", ""},
		"wrong point":    {"        binds: art-neutral/artifact-revision.operations.capture\n        at: evaluation", "        binds: art-neutral/artifact-revision.operations.capture\n        at: client", "runs at evaluation, but the binder runs at client"},
	} {
		pkgs := loadExample(t, "art-neutral")
		pkgs["art-git"] = with(t, "art-git", strings.Replace(base, tc.from, tc.to, 1))
		errs := Check(pkgs)
		joined := strings.Join(errs, "\n")
		if tc.want == "" {
			// An extra input the abstract operation does not take is accepted when it is optional.
			if joined != "" {
				t.Errorf("%s: %v", name, errs)
			}
			continue
		}
		if !strings.Contains(joined, tc.want) {
			t.Errorf("%s: got %v, want %q", name, errs, tc.want)
		}
	}
}

func TestAbstractOperationHasNoEffect(t *testing.T) {
	src := strings.Replace(sourceOf(t, "art-neutral"), "        returns: \"The retained revision and what was observed.\"", "        effect: \"Does something.\"\n        returns: \"The retained revision and what was observed.\"", 1)
	pkgs := map[string]*Package{"art-neutral": with(t, "art-neutral", src)}
	if errs := Check(pkgs); !strings.Contains(strings.Join(errs, "\n"), "an abstract operation has no effect of its own") {
		t.Fatalf("got %v", errs)
	}
}

func TestBindingANonAbstractOperationIsRefused(t *testing.T) {
	src := strings.Replace(sourceOf(t, "art-neutral"), "        abstract: true\n", "", 1)
	pkgs := loadExample(t, "art-git")
	pkgs["art-neutral"] = with(t, "art-neutral", src)
	if errs := Check(pkgs); !strings.Contains(strings.Join(errs, "\n"), "is not abstract, so nothing binds it") {
		t.Fatalf("got %v", errs)
	}
}

func TestBinderMayNotRequireWhatTheAbstractOperationLeavesOptional(t *testing.T) {
	neutral := strings.Replace(sourceOf(t, "art-neutral"),
		"          operation_id: { type: string, required: true, meaning: \"A caller-supplied operation identity.\" }\n",
		"          operation_id: { type: string, required: true, meaning: \"A caller-supplied operation identity.\" }\n          note: { type: string }\n", 1)
	git := strings.Replace(sourceOf(t, "art-git"),
		"          operation_id: { type: string, required: true }\n",
		"          operation_id: { type: string, required: true }\n          note: { type: string, required: true }\n", 1)
	pkgs := map[string]*Package{"art-neutral": with(t, "art-neutral", neutral), "art-git": with(t, "art-git", git)}
	errs := Check(pkgs)
	if !strings.Contains(strings.Join(errs, "\n"), "input note is required by the binder but optional") {
		t.Fatalf("got %v", errs)
	}
	// The same binder that leaves the input optional is accepted.
	git = strings.Replace(git, "note: { type: string, required: true }", "note: { type: string }", 1)
	pkgs["art-git"] = with(t, "art-git", git)
	if errs := Check(pkgs); len(errs) > 0 {
		t.Fatalf("optional input refused: %v", errs)
	}
}
