package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/altakleos/strux/format"
)

func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){"strux": main})
}

// The scripts in testdata/script are transcripts of strux as an agent sees it.
func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir:                 "testdata/script",
		RequireExplicitExec: true,
		RequireUniqueNames:  true,
		Setup:               withVectors,
	})
}

// scriptVectors are the verification vectors a script can read as logs.
var scriptVectors = []string{"displaced-revision", "read-behind"}

// withVectors copies each script vector's log into the script's work directory, writes its
// initial trust root beside it as <vector>.trust-root.json, and sets, for each case that relies
// on a use, <CASE>_USE and <CASE>_SUBJECT to the use and the subject a command line names. A case
// that reads fewer entries than the log holds reads a copy of the log, <vector>-<n>, that holds
// only those, as a store that lags does.
func withVectors(env *testscript.Env) error {
	for _, name := range scriptVectors {
		src := filepath.Join("..", "..", "verify", "testdata", "vectors", name)
		dst := filepath.Join(env.WorkDir, name)
		if err := copyTree(os.DirFS(src), dst); err != nil {
			return err
		}
		data, err := os.ReadFile(filepath.Join(dst, "cases.json"))
		if err != nil {
			return err
		}
		v, err := format.ParseJSON(data)
		if err != nil {
			return err
		}
		for i, c := range v.(map[string]any)["cases"].([]any) {
			inputs := c.(map[string]any)["inputs"].(map[string]any)
			if i == 0 {
				root, err := json.Marshal(inputs["trust_root_configuration"].(map[string]any)["initial"])
				if err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(env.WorkDir, name+".trust-root.json"), root, 0o600); err != nil {
					return err
				}
			}
			if err := lagging(dst, name, env.WorkDir, inputs); err != nil {
				return err
			}
			reliance, ok := inputs["reliance"].(map[string]any)
			if !ok {
				continue
			}
			recorded := reliance["subject"].(map[string]any)["recorded"].(map[string]any)
			prefix := strings.ToUpper(strings.ReplaceAll(c.(map[string]any)["case"].(string), "-", "_"))
			env.Setenv(prefix+"_USE", reliance["use"].(string))
			env.Setenv(prefix+"_SUBJECT", recorded["fact"].(string)+":"+recorded["digest"].(string))
		}
	}
	return nil
}

func copyTree(src fs.FS, dst string) error {
	return fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dst, filepath.FromSlash(path))
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		data, err := fs.ReadFile(src, path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
}

// lagging writes <vector>-<n>, a copy of the log that holds only the first n records, where a
// case reads n entries and the log holds more.
func lagging(src, name, work string, inputs map[string]any) error {
	size := inputs["log"].(map[string]any)["size"].(int64)
	records, err := os.ReadFile(filepath.Join(src, "records.jsonl"))
	if err != nil {
		return err
	}
	lines := format.Records(records)
	if int(size) >= len(lines) {
		return nil
	}
	dst := filepath.Join(work, name+"-"+strconv.FormatInt(size, 10))
	if err := copyTree(os.DirFS(src), dst); err != nil {
		return err
	}
	var kept []byte
	for _, line := range lines[:size] {
		kept = append(append(kept, line...), '\n')
	}
	return os.WriteFile(filepath.Join(dst, "records.jsonl"), kept, 0o600)
}
