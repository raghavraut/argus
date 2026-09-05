package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raghavraut/argus/internal/core"
	"github.com/raghavraut/argus/internal/state"
)

// BYOS: amass/subfinder pastes tolerate comments, raw URLs, ports, dupes.
func TestLoadTargetsBYOS(t *testing.T) {
	dir := t.TempDir()
	list := "# recon 2026-09-06\n" +
		"https://admin.target.com/login?x=1 # main entry\n" +
		"// stale note\n" +
		"\n" +
		"API.target.com.\n" +
		"admin.target.com\n" +
		"dev.target.com:8443\n"
	path := filepath.Join(dir, "subs.txt")
	if err := os.WriteFile(path, []byte(list), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := loadTargets("", path, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"admin.target.com", "api.target.com", "dev.target.com:8443"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func seedFilterDB(t *testing.T, path string) {
	t.Helper()
	s, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	rows := []state.ResultRow{
		{Result: core.TriageResult{Asset: "admin.t", FinalScore: 0.85, Confidence: 0.95, Evidence: []string{"admin+login"}, Recommendation: "manual_review_high_priority", Final: true},
			Status: 200, Title: "Admin Login", Tech: []string{"Jenkins"}},
		{Result: core.TriageResult{Asset: "waf.t", FinalScore: 0.02, Confidence: 0.5, Recommendation: "ignore", Final: true},
			Status: 403, Title: "Forbidden", Tech: []string{"Cloudflare"}},
	}
	if err := s.SaveResults(t.Context(), "c1", rows); err != nil {
		t.Fatal(err)
	}
	s.MarkDone("c1", "admin.t", "probe")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
}

func runFilterCmd(t *testing.T, db string, args ...string) string {
	t.Helper()
	root := NewRoot()
	var b bytes.Buffer
	root.SetOut(&b)
	root.SetErr(&b)
	full := append([]string{"filter", "--db", db, "--campaign", "c1"}, args...)
	root.SetArgs(full)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestFilterURLs(t *testing.T) {
	db := filepath.Join(t.TempDir(), "f.db")
	seedFilterDB(t, db)
	out := runFilterCmd(t, db, "--min-score", "0.6", "--format", "urls")
	if out != "https://admin.t\n" {
		t.Fatalf("urls output %q", out)
	}
}

func TestFilterJSONL(t *testing.T) {
	db := filepath.Join(t.TempDir(), "f.db")
	seedFilterDB(t, db)
	out := runFilterCmd(t, db, "--tech", "jenkins", "--format", "jsonl")
	if !strings.Contains(out, `"asset":"admin.t"`) || strings.Contains(out, "waf.t") {
		t.Fatalf("jsonl output %q", out)
	}
}

func TestFilterMarkdown(t *testing.T) {
	db := filepath.Join(t.TempDir(), "f.db")
	seedFilterDB(t, db)
	out := runFilterCmd(t, db, "--format", "markdown")
	for _, want := range []string{"| Asset |", "admin.t", "waf.t", "0.850"} {
		if !strings.Contains(out, want) {
			t.Fatalf("markdown missing %q:\n%s", want, out)
		}
	}
}

func TestFilterBadFormat(t *testing.T) {
	db := filepath.Join(t.TempDir(), "f.db")
	seedFilterDB(t, db)
	root := NewRoot()
	var b bytes.Buffer
	root.SetOut(&b)
	root.SetErr(&b)
	root.SetArgs([]string{"filter", "--db", db, "--format", "yaml"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected error for bad format")
	}
}
