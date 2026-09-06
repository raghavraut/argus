// Command rarefy is the Project Rarefy entry point.
//
// Contract: strict JSONL on stdout (provisional Final=false during Phase-1,
// reranked Final=true after the TF-IDF pass), all logs on stderr.
// Interrupted runs resume from SQLite without re-probing.
package main

import (
	"os"

	"github.com/raghavraut/rarefy/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
