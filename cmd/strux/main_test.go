package main

import (
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
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
	})
}
