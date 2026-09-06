// Package cli implements the Cobra command tree.
//
// Ghost-flag quarantine: several ProjectDiscovery libraries register stdlib
// `flag` flags (e.g. -rod, -headless) via init() as a side effect of being
// imported as libraries. Cobra uses pflag, which is a separate flag set, so
// those stdlib registrations can never leak into our help — we deliberately
// never call pflag.AddGoFlagSet(flag.CommandLine). GhostFlags() enumerates
// the quarantined names so tests can prove the help stays clean.
package cli

import (
	"flag"
	"sort"

	"github.com/spf13/cobra"
)

// banner is the Rarefy ASCII wordmark shown in help output.
const banner = "_|        \n" +
	"   __|  _` |   __|  _ \\  |    |   | \n" +
	"  |    (   |  |     __/  __|  |   | \n" +
	" _|   \\__,_| _|   \\___| _|   \\__, | \n" +
	"                             ____/  "

// NewRoot builds the `rarefy` command tree.
func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "rarefy",
		Short: "Campaign-aware recon triage engine",
		Long: banner + "\n\n" +
			`Strict JSONL goes to stdout for Unix pipelines; all logs go to stderr.`,
		SilenceUsage: true,
	}
	root.AddCommand(newScan(), newExport(), newFilter(), newEval(), newUI(), newDB())
	return root
}

// Execute runs the CLI (called from cmd/rarefy/main.go).
// version is the build stamp shown by `rarefy --version`.
func Execute(version string) error {
	root := NewRoot()
	if version != "" {
		root.Version = version
	}
	return root.Execute()
}

// GhostFlags returns the stdlib flag names registered by third-party
// libraries (ProjectDiscovery et al.) that are quarantined out of our CLI.
// Cobra/pflag never sees them, so they cannot appear in help or parsing.
func GhostFlags() []string {
	var out []string
	flag.CommandLine.VisitAll(func(f *flag.Flag) {
		out = append(out, f.Name)
	})
	sort.Strings(out)
	return out
}
