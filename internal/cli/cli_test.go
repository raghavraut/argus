package cli

import (
	"bytes"
	"strings"
	"testing"
)

// The v0.1 stdlib-flag CLI leaked ProjectDiscovery init() flags (-rod,
// -headless, ...) into help. Cobra/pflag must never show them.
func TestHelpHasNoGhostFlags(t *testing.T) {
	ghosts := GhostFlags()
	if len(ghosts) == 0 {
		t.Log("no stdlib flags registered by deps in this build; quarantine is vacuous")
	}
	root := NewRoot()
	var b bytes.Buffer
	root.SetOut(&b)
	root.SetErr(&b)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	help := b.String()
	for _, sub := range []string{"scan", "export", "db"} {
		if !strings.Contains(help, sub) {
			t.Fatalf("root help missing subcommand %q:\n%s", sub, help)
		}
	}
	for _, g := range ghosts {
		if strings.Contains(help, "-"+g) {
			t.Fatalf("ghost flag -%s leaked into help:\n%s", g, help)
		}
	}
	// Spot-check the known PD offenders regardless of registration.
	for _, known := range []string{"-rod", "-headless"} {
		if strings.Contains(help, known) {
			t.Fatalf("known ghost %s leaked into help:\n%s", known, help)
		}
	}
}

func TestScanHelpListsCorpusFlag(t *testing.T) {
	root := NewRoot()
	var b bytes.Buffer
	root.SetOut(&b)
	root.SetErr(&b)
	root.SetArgs([]string{"scan", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if help := b.String(); !strings.Contains(help, "--export-corpus") {
		t.Fatalf("scan help missing --export-corpus:\n%s", help)
	}
}
