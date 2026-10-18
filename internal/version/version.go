// Package version describes a strux build, from what the Go toolchain records in the binary.
// strux version reports it, and check-release reads the report with the same type.
package version

import (
	"runtime"
	"runtime/debug"
)

// Info describes a build. go build takes the module version from the git tag on the commit it
// builds and records that commit, so a release reports its tag and any other build a
// pseudo-version; go test records neither.
type Info struct {
	Version    string `json:"version"`
	Commit     string `json:"commit,omitempty"`
	CommitTime string `json:"commit_time,omitempty"`
	Modified   bool   `json:"modified,omitempty"` // built with uncommitted changes
	Go         string `json:"go"`
	Platform   string `json:"platform"`
}

// Read describes the running binary.
func Read() Info {
	info, _ := debug.ReadBuildInfo()
	return describe(info)
}

func describe(info *debug.BuildInfo) Info {
	v := Info{Version: "(devel)", Go: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH}
	if info == nil {
		return v
	}
	if info.Main.Version != "" {
		v.Version = info.Main.Version
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			v.Commit = s.Value
		case "vcs.time":
			v.CommitTime = s.Value
		case "vcs.modified":
			v.Modified = s.Value == "true"
		}
	}
	return v
}
