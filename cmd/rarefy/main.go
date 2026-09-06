// Command rarefy is the Project Rarefy entry point.
//
// Contract: strict JSONL on stdout (provisional Final=false during Phase-1,
// reranked Final=true after the TF-IDF pass), all logs on stderr.
// Interrupted runs resume from SQLite without re-probing.
package main

import (
	"os"
	"runtime/debug"

	"github.com/raghavraut/rarefy/internal/cli"
)

// Stamped by GoReleaser ldflags at release time; "dev" for local builds.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := cli.Execute(resolveVersion()); err != nil {
		os.Exit(1)
	}
}

// resolveVersion prefers release stamps, then the module version Go
// records for `go install pkg@version` builds, then the dev fallback.
func resolveVersion() string {
	if version != "dev" {
		return version + " (" + commit + " " + date + ")"
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "dev (local build)"
}
