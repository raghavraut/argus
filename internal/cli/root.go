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
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"
)

// banner is the Rarefy ASCII wordmark shown in help output.
// Row 1 carries a 26-space indent (36-column grid); trailing spaces are
// significant — banner_test.go locks the exact bytes.
const banner = "                          _|        \n" +
	"   __|  _` |   __|  _ \\  |    |   | \n" +
	"  |    (   |  |     __/  __|  |   | \n" +
	" _|   \\__,_| _|   \\___| _|   \\__, | \n" +
	"                             ____/  "

// ANSI styles for interactive output. Plain when NO_COLOR is set or the
// terminal is dumb — help text stays pipe-safe, colors only touch the
// bare-run greeting, which is human-facing by construction.
const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiCyan   = "\x1b[36m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiDim    = "\x1b[2m"
)

func colorsOn() bool {
	return os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
}

func paint(code, s string) string {
	if !colorsOn() {
		return s
	}
	return code + s + ansiReset
}

func styleBanner(s string) string { return paint(ansiBold+ansiCyan, s) }
func styleError(s string) string  { return paint(ansiBold+ansiRed, s) }
func styleGood(s string) string   { return paint(ansiGreen, s) }
func styleWarn(s string) string   { return paint(ansiYellow, s) }
func styleDim(s string) string    { return paint(ansiDim, s) }
func styleCmd(s string) string    { return paint(ansiBold+ansiGreen, s) }

// rootHelpTemplate is the default cobra help plus every subcommand's full
// flag listing, so `rarefy --help` is the complete reference in one place.
const rootHelpTemplate = `{{with .Long}}{{. | trimTrailingWhitespaces}}{{end}}

Usage:
  {{.UseLine}}

Available Commands:{{range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}

Flags for every command:{{range .Commands}}{{if .IsAvailableCommand}}

  {{.Name}}:{{if .Short}} {{.Short}}{{end}}
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{end}}

Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}

Use "{{.CommandPath}} [command] --help" for more information about a command.
`

// NewRoot builds the `rarefy` command tree.
func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "rarefy",
		Short: "Campaign-aware recon triage engine",
		Long: banner + "\n\n" +
			`Strict JSONL goes to stdout for Unix pipelines; all logs go to stderr.`,
		SilenceUsage: true,
		// Bare `rarefy` is a usage error, not a help request: art, then a
		// one-line error plus the update nudge — never the full guide
		// (that's what -h is for).
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := cmd.ErrOrStderr()
			fmt.Fprintln(w, styleBanner(banner))
			fmt.Fprintln(w)
			fmt.Fprintln(w, styleError("Error: missing command — see 'rarefy --help' for usage."))
			printVersionNotice(w, cmd.Root().Version)
			return errors.New("missing command")
		},
	}
	root.SetHelpTemplate(rootHelpTemplate)
	root.AddCommand(newScan(), newExport(), newFilter(), newEval(), newUI(), newDB())
	// Cobra children inherit the parent's help template; pin them back to
	// stock rendering so only the root shows the every-command listing.
	// (String copied from cobra v1.10.2's defaultHelpTemplate; bump with go.mod.)
	for _, sub := range root.Commands() {
		sub.SetHelpTemplate(`{{with (or .Long .Short)}}{{. | trimTrailingWhitespaces}}

{{end}}{{if or .Runnable .HasSubCommands}}{{.UsageString}}{{end}}`)
	}
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
