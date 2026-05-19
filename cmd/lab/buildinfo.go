package main

import "runtime/debug"

// buildVersion, buildCommit, buildTime are injected via -ldflags at build time.
// The init() below fills commit and time from embedded VCS info when ldflags
// didn't override them — so binaries built without explicit ldflags still
// report a sensible version.
var (
	buildVersion = "dev"
	buildCommit  = ""
	buildTime    = "unknown"
)

func init() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if buildCommit == "" && len(s.Value) >= 7 {
				buildCommit = s.Value[:7]
			}
		case "vcs.time":
			if buildTime == "unknown" {
				buildTime = s.Value
			}
		}
	}
}
