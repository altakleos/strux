// Command check-release checks release assets: the archives in a directory and its checksums.txt.
//
// With -matrix it prints, as a GitHub Actions matrix, the runner for each archive's platform, so
// that every archive built is run. A platform with no runner is an error.
//
// Otherwise it checks the archive for -platform, on a machine of that platform. The archive's
// SHA-256 digest must match checksums.txt; its strux binary must run and report, through
// strux version --json, that platform, the expected commit, no uncommitted changes, and, for a
// tagged release, the tag as its version; and its THIRD_PARTY_NOTICES must list the Go release
// and every module the binary was built with.
//
// Usage:
//
//	go run ./internal/cmd/check-release -dir DIR -matrix
//	go run ./internal/cmd/check-release -dir DIR -platform OS/ARCH -commit SHA [-tag vX.Y.Z]
package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/altakleos/strux/internal/version"
)

// runners names the GitHub-hosted runner that runs each platform's binaries natively.
var runners = map[string]string{
	"darwin/amd64":  "macos-15-intel",
	"darwin/arm64":  "macos-15",
	"linux/amd64":   "ubuntu-24.04",
	"linux/arm64":   "ubuntu-24.04-arm",
	"windows/amd64": "windows-2025",
	"windows/arm64": "windows-11-arm",
}

func main() {
	dir := flag.String("dir", "dist", "directory holding the archives and checksums.txt")
	matrix := flag.Bool("matrix", false, "print the runner for each archive's platform")
	platform := flag.String("platform", "", "platform of the archive to check, as OS/ARCH")
	commit := flag.String("commit", "", "commit the binary must report")
	tag := flag.String("tag", "", "for a tagged release, the version the binary must report")
	flag.Parse()
	var report string
	var err error
	switch {
	case flag.NArg() > 0:
		usage()
	case *matrix:
		if *platform != "" || *commit != "" || *tag != "" {
			usage()
		}
		report, err = runnerMatrix(*dir)
	default:
		if *platform == "" || *commit == "" {
			usage()
		}
		report, err = check(*dir, *platform, *commit, *tag)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "check-release:", err)
		os.Exit(1)
	}
	fmt.Println(report)
}

func usage() {
	flag.Usage()
	os.Exit(2)
}

// runnerMatrix is a GitHub Actions matrix that runs each archive's platform on its runner.
func runnerMatrix(dir string) (string, error) {
	sums, err := readChecksums(filepath.Join(dir, "checksums.txt"))
	if err != nil {
		return "", err
	}
	all, err := archives(sums)
	if err != nil {
		return "", err
	}
	type job struct {
		Platform string `json:"platform"`
		Runner   string `json:"runner"`
	}
	var jobs []job
	for platform := range all {
		runner, ok := runners[platform]
		if !ok {
			return "", fmt.Errorf("no runner for %s; add one to check-release", platform)
		}
		jobs = append(jobs, job{platform, runner})
	}
	slices.SortFunc(jobs, func(a, b job) int { return strings.Compare(a.Platform, b.Platform) })
	m, err := json.Marshal(map[string][]job{"include": jobs})
	return string(m), err
}

func check(dir, platform, commit, tag string) (string, error) {
	if here := runtime.GOOS + "/" + runtime.GOARCH; platform != here {
		return "", fmt.Errorf("this machine is %s, not %s", here, platform)
	}
	sums, err := readChecksums(filepath.Join(dir, "checksums.txt"))
	if err != nil {
		return "", err
	}
	all, err := archives(sums)
	if err != nil {
		return "", err
	}
	a, ok := all[platform]
	if !ok {
		return "", fmt.Errorf("checksums.txt lists no archive for %s", platform)
	}
	if tag != "" && a.version != strings.TrimPrefix(tag, "v") {
		return "", fmt.Errorf("%s is not named for %s", a.name, tag)
	}
	path := filepath.Join(dir, a.name)
	if err := checkDigest(path, sums[a.name]); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp("", "check-release-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	bin, notices, err := unpack(path, a.platform, tmp)
	if err != nil {
		return "", fmt.Errorf("%s: %w", a.name, err)
	}
	got, err := report(bin)
	if err != nil {
		return "", fmt.Errorf("%s: %w", a.name, err)
	}
	var problems []string
	if got.Platform != platform {
		problems = append(problems, fmt.Sprintf("reports platform %q, want %q", got.Platform, platform))
	}
	if got.Commit != commit {
		problems = append(problems, fmt.Sprintf("reports commit %q, want %q", got.Commit, commit))
	}
	if got.Modified {
		problems = append(problems, "was built with uncommitted changes")
	}
	switch {
	case tag != "" && got.Version != tag:
		problems = append(problems, fmt.Sprintf("reports version %q, want %q", got.Version, tag))
	case got.Version == "(devel)":
		problems = append(problems, "reports no version")
	}
	if missing, err := unlisted(bin, notices); err != nil {
		return "", fmt.Errorf("%s: %w", a.name, err)
	} else if len(missing) > 0 {
		problems = append(problems, "includes code that THIRD_PARTY_NOTICES does not list: "+strings.Join(missing, ", "))
	}
	if len(problems) > 0 {
		return "", fmt.Errorf("%s: strux %s", a.name, strings.Join(problems, "; "))
	}
	return fmt.Sprintf("%s: digest matches checksums.txt; strux %s, commit %s, %s", a.name, got.Version, got.Commit, got.Platform), nil
}

// readChecksums reads a checksums file: one "<sha256 hex>  <file name>" line per asset.
func readChecksums(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sums := map[string]string{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		sum, name, ok := strings.Cut(s.Text(), "  ")
		if !ok || len(sum) != sha256.Size*2 {
			return nil, fmt.Errorf("%s: malformed line %q", path, s.Text())
		}
		sums[name] = sum
	}
	return sums, s.Err()
}

// archive is a release archive, named strux_<version>_<os>_<arch>: a zip for Windows and a
// gzipped tar otherwise. A tagged release's version is its tag without the v.
type archive struct {
	name, version, platform string
}

// archives reads the archive for each platform from the assets that checksums.txt lists, which
// must all be archives, one for each platform.
func archives(sums map[string]string) (map[string]archive, error) {
	all := map[string]archive{}
	for name := range sums {
		base, ok := strings.CutSuffix(name, ".tar.gz")
		isZip := false
		if !ok {
			base, ok = strings.CutSuffix(name, ".zip")
			isZip = true
		}
		parts := strings.Split(base, "_")
		if !ok || len(parts) != 4 || parts[0] != "strux" || isZip != (parts[2] == "windows") {
			return nil, fmt.Errorf("checksums.txt lists %s, which is not a strux archive", name)
		}
		a := archive{name, parts[1], parts[2] + "/" + parts[3]}
		if other, ok := all[a.platform]; ok {
			return nil, fmt.Errorf("checksums.txt lists two archives for %s: %s and %s", a.platform, other.name, name)
		}
		all[a.platform] = a
	}
	return all, nil
}

func checkDigest(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("%s: sha256 %s, but checksums.txt lists %s", filepath.Base(path), got, want)
	}
	return nil
}

const noticesFile = "THIRD_PARTY_NOTICES"

// unpack extracts the strux binary for a platform from an archive into dir, and returns the
// binary's path and the archive's notices.
func unpack(archive, platform, dir string) (bin string, notices []byte, err error) {
	name := "strux"
	if strings.HasPrefix(platform, "windows/") {
		name += ".exe"
	}
	extract := fromTarGz
	if strings.HasSuffix(archive, ".zip") {
		extract = fromZip
	}
	files, err := extract(archive, name, noticesFile)
	if err != nil {
		return "", nil, err
	}
	bin = filepath.Join(dir, name)
	return bin, files[noticesFile], os.WriteFile(bin, files[name], 0o700) //nolint:gosec // the binary must be executable
}

// fromZip reads the named files from a zip archive.
func fromZip(archive string, names ...string) (map[string][]byte, error) {
	z, err := zip.OpenReader(archive)
	if err != nil {
		return nil, err
	}
	defer z.Close()
	files := map[string][]byte{}
	for _, f := range z.File {
		if slices.Contains(names, f.Name) && f.Mode().IsRegular() {
			if files[f.Name], err = readZipped(f); err != nil {
				return nil, err
			}
		}
	}
	return files, missing(files, names)
}

func readZipped(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// fromTarGz reads the named files from a gzipped tar archive.
func fromTarGz(archive string, names ...string) (map[string][]byte, error) {
	f, err := os.Open(archive)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	t := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		h, err := t.Next()
		if errors.Is(err, io.EOF) {
			return files, missing(files, names)
		}
		if err != nil {
			return nil, err
		}
		if slices.Contains(names, h.Name) && h.Typeflag == tar.TypeReg {
			if files[h.Name], err = io.ReadAll(t); err != nil {
				return nil, err
			}
		}
	}
}

func missing(files map[string][]byte, names []string) error {
	for _, name := range names {
		if _, ok := files[name]; !ok {
			return fmt.Errorf("no %s in the archive", name)
		}
	}
	return nil
}

// report runs strux version --json. Its fields must be exactly those of version.Info.
func report(bin string) (version.Info, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "version", "--json")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return version.Info{}, fmt.Errorf("strux version --json: %w: %s", err, stderr.String())
	}
	var v version.Info
	dec := json.NewDecoder(&stdout)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return version.Info{}, fmt.Errorf("strux version --json: %w", err)
	}
	return v, nil
}

// unlisted names the code a binary was built with that its notices do not list on a line of its
// own: the Go standard library, as "Go standard library <go version>", and each module, as
// "<path> <version>".
func unlisted(bin string, notices []byte) ([]string, error) {
	info, err := buildinfo.ReadFile(bin)
	if err != nil {
		return nil, err
	}
	listed := map[string]bool{}
	for line := range strings.Lines(string(notices)) {
		listed[strings.TrimSpace(line)] = true
	}
	want := []string{"Go standard library " + info.GoVersion}
	for _, m := range info.Deps {
		if m.Replace != nil {
			m = m.Replace
		}
		want = append(want, m.Path+" "+m.Version)
	}
	var missing []string
	for _, w := range want {
		if !listed[w] {
			missing = append(missing, w)
		}
	}
	return missing, nil
}
