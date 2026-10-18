package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/altakleos/strux/internal/installers"
	"github.com/altakleos/strux/internal/version"
)

// When check runs this test binary from an archive, it stands in for strux and prints the
// version report the test asks for.
func TestMain(m *testing.M) {
	if report, ok := os.LookupEnv("CHECK_RELEASE_FAKE_REPORT"); ok {
		fmt.Println(report)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

var here = runtime.GOOS + "/" + runtime.GOARCH

func sums(names ...string) map[string]string {
	m := map[string]string{}
	for _, name := range names {
		m[name] = strings.Repeat("ab", 32)
	}
	return m
}

// withScripts adds the install scripts to a release's file names.
func withScripts(names ...string) []string {
	return append(names, installers.Names...)
}

func TestAssets(t *testing.T) {
	got, err := assets(sums(withScripts("strux_0.1.0_darwin_arm64.tar.gz", "strux_0.1.1-dev+843c826_windows_amd64.zip")...))
	want := map[string]archive{
		"darwin/arm64":  {"strux_0.1.0_darwin_arm64.tar.gz", "0.1.0", "darwin/arm64"},
		"windows/amd64": {"strux_0.1.1-dev+843c826_windows_amd64.zip", "0.1.1-dev+843c826", "windows/amd64"},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("assets = %v, %v; want %v", got, err, want)
	}
	for _, tc := range []struct {
		names []string
		err   string
	}{
		{withScripts("README.md"), "README.md, which is neither a strux archive nor an install script"},
		{withScripts("strux_0.1.0_linux_amd64.zip"), "neither a strux archive"},
		{withScripts("strux_0.1.0_windows_amd64.tar.gz"), "neither a strux archive"},
		{withScripts("strux_0.1.0_linux.tar.gz"), "neither a strux archive"},
		{withScripts("other_0.1.0_linux_amd64.tar.gz"), "neither a strux archive"},
		{withScripts("strux_0.1.0_linux_amd64.tar.gz", "strux_0.1.1_linux_amd64.tar.gz"), "two archives for linux/amd64"},
		{[]string{"strux_0.1.0_linux_amd64.tar.gz", "install.sh"}, "lists no install.ps1"},
		{[]string{"strux_0.1.0_linux_amd64.tar.gz", "install.ps1"}, "lists no install.sh"},
	} {
		if _, err := assets(sums(tc.names...)); err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("assets(%q) = %v, want an error containing %q", tc.names, err, tc.err)
		}
	}
}

func TestRunnerMatrix(t *testing.T) {
	dir := t.TempDir()
	writeChecksums(t, dir, sums(withScripts("strux_0.1.0_windows_arm64.zip", "strux_0.1.0_linux_amd64.tar.gz")...))
	got, err := runnerMatrix(dir)
	want := `{"include":[{"platform":"linux/amd64","runner":"ubuntu-24.04"},{"platform":"windows/arm64","runner":"windows-11-arm"}]}`
	if err != nil || got != want {
		t.Errorf("runnerMatrix = %s, %v; want %s", got, err, want)
	}
	writeChecksums(t, dir, sums(withScripts("strux_0.1.0_linux_amd64.tar.gz", "strux_0.1.0_freebsd_amd64.tar.gz")...))
	if _, err := runnerMatrix(dir); err == nil || !strings.Contains(err.Error(), "no runner for freebsd/amd64") {
		t.Errorf("runnerMatrix = %v, want an error naming freebsd/amd64", err)
	}
}

func TestReadChecksums(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	dir := t.TempDir()
	write := func(text string) string {
		path := filepath.Join(dir, "checksums.txt")
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	got, err := readChecksums(write(sum + "  strux_0.1.0_linux_amd64.tar.gz\n"))
	if err != nil || got["strux_0.1.0_linux_amd64.tar.gz"] != sum {
		t.Errorf("readChecksums = %v, %v", got, err)
	}
	for _, bad := range []string{"abc  strux.tar.gz\n", sum + " strux.tar.gz\n"} {
		if _, err := readChecksums(write(bad)); err == nil {
			t.Errorf("readChecksums accepted %q", bad)
		}
	}
}

// Both archive formats give up the files asked for, and only regular files.
func TestExtract(t *testing.T) {
	files := map[string][]byte{"strux": []byte("binary"), "THIRD_PARTY_NOTICES": []byte("notices")}
	for _, name := range []string{"a.zip", "a.tar.gz"} {
		path := filepath.Join(t.TempDir(), name)
		writeArchive(t, path, files)
		extract := fromTarGz
		if strings.HasSuffix(name, ".zip") {
			extract = fromZip
		}
		got, err := extract(path, "strux", "THIRD_PARTY_NOTICES")
		if err != nil || !reflect.DeepEqual(got, files) {
			t.Errorf("%s: extract = %q, %v", name, got, err)
		}
		if _, err := extract(path, "strux.exe"); err == nil || !strings.Contains(err.Error(), "no strux.exe in the archive") {
			t.Errorf("%s: extract strux.exe = %v", name, err)
		}
	}
}

func TestCheck(t *testing.T) {
	commit := "843c826f1e03a8a0c1d1e7b1c4a3f8c3f3c2b1a0"
	good := version.Info{Version: "v0.1.0", Commit: commit, Go: runtime.Version(), Platform: here}
	with := func(edit func(*version.Info)) version.Info {
		v := good
		edit(&v)
		return v
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// An install script that installs the archive's strux, by copying this test binary, and
	// says nothing about provenance.
	quiet := script(
		`mkdir -p "$STRUX_INSTALL_DIR" && cp '`+self+`' "$STRUX_INSTALL_DIR/strux"`,
		`New-Item -ItemType Directory -Force $env:STRUX_INSTALL_DIR | Out-Null; Copy-Item '`+self+`' (Join-Path $env:STRUX_INSTALL_DIR strux.exe)`,
	)
	for _, tc := range []struct {
		name       string
		report     any
		archive    func(files map[string][]byte)
		assets     func(files map[string][]byte)
		platform   string
		tag        string
		provenance bool
		err        string
	}{
		{"a release", good, nil, nil, here, "v0.1.0", false, ""},
		{"a snapshot", with(func(v *version.Info) { v.Version = "v0.1.1-0.20261018072757-843c826f1e03" }), nil, nil, here, "", false, ""},
		{"another platform's runner", good, nil, nil, "plan9/386", "v0.1.0", false, "this machine is " + here + ", not plan9/386"},
		{"an archive named for another tag", good, nil, nil, here, "v0.2.0", false, "is not named for v0.2.0"},
		{"another commit", with(func(v *version.Info) { v.Commit = "0123" }), nil, nil, here, "v0.1.0", false, `reports commit "0123"`},
		{"uncommitted changes", with(func(v *version.Info) { v.Modified = true }), nil, nil, here, "", false, "was built with uncommitted changes"},
		{"no version", with(func(v *version.Info) { v.Version = "(devel)" }), nil, nil, here, "", false, "reports no version"},
		{"another version", with(func(v *version.Info) { v.Version = "v0.1.0+dirty" }), nil, nil, here, "v0.1.0", false, `reports version "v0.1.0+dirty", want "v0.1.0"`},
		{"another platform's binary", with(func(v *version.Info) { v.Platform = "linux/riscv64" }), nil, nil, here, "", false, `reports platform "linux/riscv64"`},
		{"a report check-release does not know", map[string]any{"version": "v0.1.0", "commit": commit, "dirty": true}, nil, nil, here, "", false, `unknown field "dirty"`},
		{"incomplete notices", good, func(f map[string][]byte) { f[noticesFile] = []byte("none\n") }, nil, here, "", false, "THIRD_PARTY_NOTICES does not list: Go standard library"},
		{"no notices", good, func(f map[string][]byte) { delete(f, noticesFile) }, nil, here, "", false, "no THIRD_PARTY_NOTICES in the archive"},
		{"no install script", good, nil, func(f map[string][]byte) { delete(f, "install.ps1") }, here, "", false, "checksums.txt lists no install.ps1"},
		{"an install script that fails", good, nil, script("exit 3", "throw 'no'"), here, "", false, "exit status"},
		{"an install script that installs nothing", good, nil, script("echo nothing", "Write-Host nothing"), here, "", false, "installed no strux"},
		{
			"an install script that installs another strux", good, nil,
			script(
				`mkdir -p "$STRUX_INSTALL_DIR" && echo other > "$STRUX_INSTALL_DIR/strux"`,
				`New-Item -ItemType Directory -Force $env:STRUX_INSTALL_DIR | Out-Null; Set-Content (Join-Path $env:STRUX_INSTALL_DIR strux.exe) other`,
			),
			here, "", false, "installed a strux other than the archive's",
		},
		{"an install script that installs it", good, nil, quiet, here, "v0.1.0", false, ""},
		{"an install script that does not check provenance", good, nil, quiet, here, "v0.1.0", true, "did not check the archive's build provenance"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := json.Marshal(tc.report)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("CHECK_RELEASE_FAKE_REPORT", string(report))
			dir := writeRelease(t, tc.archive, tc.assets)
			_, err = check(dir, tc.platform, commit, tc.tag, tc.provenance)
			if (err == nil) != (tc.err == "") || err != nil && !strings.Contains(err.Error(), tc.err) {
				t.Errorf("check = %v, want an error containing %q", err, tc.err)
			}
		})
	}
}

// script replaces the install script for this platform: sh's text for install.sh on macOS and
// Linux, and PowerShell's for install.ps1 on Windows.
func script(sh, powershell string) func(files map[string][]byte) {
	return func(files map[string][]byte) {
		if runtime.GOOS == "windows" {
			files["install.ps1"] = []byte(powershell)
		} else {
			files["install.sh"] = []byte(sh)
		}
	}
}

// Every asset check-release checks must have the digest checksums.txt lists.
func TestCheckDigest(t *testing.T) {
	name := "strux_0.1.0_" + strings.ReplaceAll(here, "/", "_") + ".tar.gz"
	if runtime.GOOS == "windows" {
		name = strings.TrimSuffix(name, ".tar.gz") + ".zip"
	}
	zeros := strings.Repeat("00", 32)
	for _, asset := range withScripts(name) {
		dir := writeRelease(t, nil, nil)
		sums, err := readChecksums(filepath.Join(dir, "checksums.txt"))
		if err != nil {
			t.Fatal(err)
		}
		sums[asset] = zeros
		writeChecksums(t, dir, sums)
		if _, err := check(dir, here, "", "", false); err == nil || !strings.Contains(err.Error(), asset+": sha256 ") || !strings.Contains(err.Error(), "but checksums.txt lists "+zeros) {
			t.Errorf("%s: check = %v, want a digest mismatch", asset, err)
		}
	}
}

// writeRelease writes, into a new directory, a release of this test binary as strux for this
// platform, as v0.1.0: its archive, with notices that list what it includes, the install scripts,
// and checksums.txt. editArchive changes the archive's files, and editAssets the release's files,
// before they are written.
func writeRelease(t *testing.T, editArchive, editAssets func(files map[string][]byte)) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	info, err := buildinfo.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	notices := "  Go standard library " + info.GoVersion + "\n"
	for _, m := range info.Deps {
		notices += "  " + m.Path + " " + m.Version + "\n"
	}
	name, archive := "strux", "strux_0.1.0_"+strings.ReplaceAll(here, "/", "_")+".tar.gz"
	if runtime.GOOS == "windows" {
		name, archive = "strux.exe", strings.TrimSuffix(archive, ".tar.gz")+".zip"
	}
	files := map[string][]byte{name: bin, noticesFile: []byte(notices)}
	if editArchive != nil {
		editArchive(files)
	}
	dir := t.TempDir()
	writeArchive(t, filepath.Join(dir, archive), files)
	data, err := os.ReadFile(filepath.Join(dir, archive))
	if err != nil {
		t.Fatal(err)
	}
	assets, err := installers.Render("0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	assets[archive] = data
	if editAssets != nil {
		editAssets(assets)
	}
	sums := map[string]string{}
	for name, data := range assets {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		sums[name] = hex.EncodeToString(sum[:])
	}
	writeChecksums(t, dir, sums)
	return dir
}

func writeChecksums(t *testing.T, dir string, sums map[string]string) {
	t.Helper()
	var b strings.Builder
	for name, sum := range sums {
		fmt.Fprintf(&b, "%s  %s\n", sum, name)
	}
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeArchive writes files into a zip or a gzipped tar archive, as path's extension says. It
// stores them uncompressed, which is quicker for a test binary.
func writeArchive(t *testing.T, path string, files map[string][]byte) {
	t.Helper()
	var buf bytes.Buffer
	if strings.HasSuffix(path, ".zip") {
		z := zip.NewWriter(&buf)
		for name, data := range files {
			w, err := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
			if err == nil {
				_, err = w.Write(data)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		gz, err := gzip.NewWriterLevel(&buf, gzip.NoCompression)
		if err != nil {
			t.Fatal(err)
		}
		tw := tar.NewWriter(gz)
		for name, data := range files {
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write(data); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
