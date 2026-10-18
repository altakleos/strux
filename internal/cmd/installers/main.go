// Command installers writes a release's install scripts, install.sh and install.ps1, into a
// directory, with the release's version written in. GoReleaser runs it before it builds, and
// publishes the scripts with the release.
//
// Usage:
//
//	go run ./internal/cmd/installers -version VERSION -o DIR
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/altakleos/strux/internal/installers"
)

func main() {
	version := flag.String("version", "", "the release's version: its tag without the v")
	dir := flag.String("o", "", "directory to write the scripts into")
	flag.Parse()
	if *version == "" || *dir == "" || flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := write(*version, *dir); err != nil {
		fmt.Fprintln(os.Stderr, "installers:", err)
		os.Exit(1)
	}
}

func write(version, dir string) error {
	scripts, err := installers.Render(version)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	for name, text := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), text, 0o600); err != nil {
			return err
		}
	}
	return nil
}
