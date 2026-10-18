// Command notices writes the notices for the code a strux binary includes from other projects: the
// Go standard library and each module the binary was built with, as the binary itself records
// them. For each, it copies the license, notice and patent files at the root of its source.
// GoReleaser runs it on each binary it builds, and the archive holding the binary carries the
// result as THIRD_PARTY_NOTICES.
//
// Usage: go run ./internal/cmd/notices -o FILE BINARY
package main

import (
	"bytes"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

func main() {
	out := flag.String("o", "", "file to write the notices to")
	flag.Parse()
	if *out == "" || flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	if err := write(*out, flag.Arg(0)); err != nil {
		fmt.Fprintln(os.Stderr, "notices:", err)
		os.Exit(1)
	}
}

func write(out, bin string) error {
	srcs, err := sources(bin)
	if err != nil {
		return err
	}
	text, err := render(srcs)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
		return err
	}
	return os.WriteFile(out, text, 0o600)
}

// source is code a binary includes from another project: its name, its version, and the
// directory holding its source.
type source struct {
	name, version, dir string
}

// sources finds the source of the Go standard library and of each module a binary was built
// with, in the Go installation that built it and in the module cache.
func sources(bin string) ([]source, error) {
	info, err := buildinfo.ReadFile(bin)
	if err != nil {
		return nil, err
	}
	dec, err := goJSON("env", "-json", "GOROOT", "GOVERSION")
	if err != nil {
		return nil, err
	}
	var env struct{ GOROOT, GOVERSION string }
	if err := dec.Decode(&env); err != nil {
		return nil, err
	}
	if env.GOVERSION != info.GoVersion {
		return nil, fmt.Errorf("%s was built with %s, but go is %s", bin, info.GoVersion, env.GOVERSION)
	}
	if len(info.Deps) == 0 {
		return []source{{"Go standard library", info.GoVersion, env.GOROOT}}, nil
	}
	args := []string{"mod", "download", "-json"}
	for _, m := range info.Deps {
		if m.Replace != nil {
			m = m.Replace
		}
		if m.Version == "" {
			return nil, fmt.Errorf("%s was built with %s from a directory, not a module version", bin, m.Path)
		}
		args = append(args, m.Path+"@"+m.Version)
	}
	if dec, err = goJSON(args...); err != nil {
		return nil, err
	}
	var srcs []source
	for dec.More() {
		var m struct{ Path, Version, Dir, Error string }
		if err := dec.Decode(&m); err != nil {
			return nil, err
		}
		if m.Error != "" {
			return nil, fmt.Errorf("%s@%s: %s", m.Path, m.Version, m.Error)
		}
		srcs = append(srcs, source{m.Path, m.Version, m.Dir})
	}
	slices.SortFunc(srcs, func(a, b source) int { return strings.Compare(a.name, b.name) })
	return append([]source{{"Go standard library", info.GoVersion, env.GOROOT}}, srcs...), nil
}

// goJSON runs go with args and returns a decoder for the JSON it prints.
func goJSON(args ...string) (*json.Decoder, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("go", args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return json.NewDecoder(&stdout), nil
}

// noticePrefixes begin the names of the files that carry a project's license and notices.
var noticePrefixes = []string{"LICENSE", "LICENCE", "COPYING", "NOTICE", "PATENTS"}

// render lists the sources, then copies each one's license and notice files.
func render(srcs []source) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("This archive's strux binary includes code from the projects below, under the license\nand notice files that follow.\n\n")
	for _, s := range srcs {
		fmt.Fprintf(&b, "  %s %s\n", s.name, s.version)
	}
	rule := strings.Repeat("=", 80)
	for _, s := range srcs {
		entries, err := os.ReadDir(s.dir)
		if err != nil {
			return nil, err
		}
		found := false
		for _, e := range entries {
			name := strings.ToUpper(e.Name())
			if !e.Type().IsRegular() || !slices.ContainsFunc(noticePrefixes, func(p string) bool { return strings.HasPrefix(name, p) }) {
				continue
			}
			text, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(&b, "\n%s\n%s %s: %s\n%s\n\n", rule, s.name, s.version, e.Name(), rule)
			b.Write(text)
			if !bytes.HasSuffix(text, []byte("\n")) {
				b.WriteByte('\n')
			}
			found = true
		}
		if !found {
			return nil, errors.New(s.name + " " + s.version + " has no license file")
		}
	}
	return b.Bytes(), nil
}
