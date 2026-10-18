package version

import (
	"runtime"
	"runtime/debug"
	"testing"
)

func TestDescribe(t *testing.T) {
	platform := runtime.GOOS + "/" + runtime.GOARCH
	for _, tc := range []struct {
		name string
		info *debug.BuildInfo
		want Info
	}{
		{"no build info", nil, Info{Version: "(devel)", Go: runtime.Version(), Platform: platform}},
		{"go test", &debug.BuildInfo{}, Info{Version: "(devel)", Go: runtime.Version(), Platform: platform}},
		{
			"release",
			&debug.BuildInfo{
				Main: debug.Module{Version: "v0.1.0"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "843c826f1e03"},
					{Key: "vcs.time", Value: "2026-10-18T07:27:57Z"},
					{Key: "vcs.modified", Value: "false"},
				},
			},
			Info{Version: "v0.1.0", Commit: "843c826f1e03", CommitTime: "2026-10-18T07:27:57Z", Go: runtime.Version(), Platform: platform},
		},
		{
			"uncommitted changes",
			&debug.BuildInfo{
				Main:     debug.Module{Version: "v0.1.0+dirty"},
				Settings: []debug.BuildSetting{{Key: "vcs.modified", Value: "true"}},
			},
			Info{Version: "v0.1.0+dirty", Modified: true, Go: runtime.Version(), Platform: platform},
		},
	} {
		if got := describe(tc.info); got != tc.want {
			t.Errorf("%s: describe = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}
