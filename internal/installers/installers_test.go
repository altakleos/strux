package installers

import (
	"bytes"
	"strings"
	"testing"
)

// Each script names the version it installs, in the place it reads it from, and is otherwise
// unchanged.
func TestRender(t *testing.T) {
	for _, version := range []string{"0.1.0", "0.1.1-dev+843c826"} {
		got, err := Render(version)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(Names) {
			t.Errorf("Render(%q) gives %d scripts, want %d", version, len(got), len(Names))
		}
		for _, name := range Names {
			template, err := scripts.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if want := bytes.ReplaceAll(template, []byte(marker), []byte(version)); !bytes.Equal(got[name], want) {
				t.Errorf("Render(%q) gives a %s that differs from its template in more than the version", version, name)
			}
		}
		if line := "version='" + version + "'\n"; !strings.Contains(string(got["install.sh"]), line) {
			t.Errorf("install.sh for %s has no line %q", version, line)
		}
		if line := "$version = '" + version + "'\n"; !strings.Contains(string(got["install.ps1"]), line) {
			t.Errorf("install.ps1 for %s has no line %q", version, line)
		}
	}
}

func TestRenderRequiresAReleaseVersion(t *testing.T) {
	for _, version := range []string{"", "v0.1.0", "0.1", "0.1.0'; rm -rf ~; '", "latest"} {
		if _, err := Render(version); err == nil || !strings.Contains(err.Error(), "is not a release version") {
			t.Errorf("Render(%q) = %v, want an error", version, err)
		}
	}
}
