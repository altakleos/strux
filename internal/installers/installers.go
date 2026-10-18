// Package installers holds the install scripts published with each release: install.sh for
// macOS and Linux and install.ps1 for Windows. Each installs the release it is published with,
// whose version Render writes in, so a release's scripts never change once published.
package installers

import (
	"bytes"
	"embed"
	"fmt"
	"regexp"
)

//go:embed install.sh install.ps1
var scripts embed.FS

// Names are the scripts' file names, which are also their names in a release.
var Names = []string{"install.sh", "install.ps1"}

// marker is where each script names the version it installs.
const marker = "@VERSION@"

// A release's version as GoReleaser names it, its tag without the v: X.Y.Z, or a snapshot's
// X.Y.Z-dev+COMMIT. The scripts quote it, so it holds nothing a quote would have to escape.
var releaseVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// Render returns each script, by name, with version written in.
func Render(version string) (map[string][]byte, error) {
	if !releaseVersion.MatchString(version) {
		return nil, fmt.Errorf("%q is not a release version", version)
	}
	rendered := map[string][]byte{}
	for _, name := range Names {
		text, err := scripts.ReadFile(name)
		if err != nil {
			return nil, err
		}
		if n := bytes.Count(text, []byte(marker)); n != 1 {
			return nil, fmt.Errorf("%s names %s %d times, not once", name, marker, n)
		}
		rendered[name] = bytes.Replace(text, []byte(marker), []byte(version), 1)
	}
	return rendered, nil
}
