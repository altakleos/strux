package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// dir writes files into a new directory, and returns it.
func dir(t *testing.T, files map[string]string) string {
	t.Helper()
	d := t.TempDir()
	for name, text := range files {
		path := filepath.Join(d, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// Each source's license, notice and patent files are copied, in order, and nothing else.
func TestRender(t *testing.T) {
	goroot := dir(t, map[string]string{"LICENSE": "Go's license.\n", "PATENTS": "Go's patent grant.", "README.md": "Go"})
	mod := dir(t, map[string]string{
		"LICENSE.txt":        "The module's license.\n",
		"NOTICE":             "The module's notice.\n",
		"go.mod":             "module example.com/mod\n",
		"licenses/LICENSE":   "A license in a subdirectory.\n",
		"internal/NOTICE.md": "A notice in a subdirectory.\n",
	})
	got, err := render([]source{{"Go standard library", "go1.27.2", goroot}, {"example.com/mod", "v1.2.3", mod}})
	if err != nil {
		t.Fatal(err)
	}
	rule := strings.Repeat("=", 80)
	want := `This archive's strux binary includes code from the projects below, under the license
and notice files that follow.

  Go standard library go1.27.2
  example.com/mod v1.2.3

` + rule + `
Go standard library go1.27.2: LICENSE
` + rule + `

Go's license.

` + rule + `
Go standard library go1.27.2: PATENTS
` + rule + `

Go's patent grant.

` + rule + `
example.com/mod v1.2.3: LICENSE.txt
` + rule + `

The module's license.

` + rule + `
example.com/mod v1.2.3: NOTICE
` + rule + `

The module's notice.
`
	if string(got) != want {
		t.Errorf("render =\n%s\nwant\n%s", got, want)
	}
}

// Code with no license cannot be shipped.
func TestRenderRequiresALicense(t *testing.T) {
	mod := dir(t, map[string]string{"README.md": "no license"})
	if _, err := render([]source{{"example.com/mod", "v1.2.3", mod}}); err == nil || !strings.Contains(err.Error(), "example.com/mod v1.2.3 has no license file") {
		t.Errorf("render = %v, want an error naming example.com/mod", err)
	}
}

// The sources of a strux binary are the Go standard library and the modules it was built with.
func TestSourcesOfStrux(t *testing.T) {
	if testing.Short() {
		t.Skip("builds strux")
	}
	bin := filepath.Join(t.TempDir(), "strux")
	build := exec.Command("go", "build", "-trimpath", "-o", bin, "github.com/altakleos/strux/cmd/strux")
	build.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	srcs, err := sources(bin)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range srcs {
		names = append(names, s.name)
		if _, err := os.Stat(s.dir); err != nil {
			t.Errorf("%s %s: %v", s.name, s.version, err)
		}
	}
	// mousetrap is part of strux on Windows only.
	want := []string{"Go standard library", "github.com/inconshreveable/mousetrap", "github.com/spf13/cobra", "github.com/spf13/pflag", "go.yaml.in/yaml/v3"}
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Errorf("sources = %q, want %q", names, want)
	}
	if srcs[0].version != runtime.Version() {
		t.Errorf("Go standard library %s, want %s", srcs[0].version, runtime.Version())
	}
}
