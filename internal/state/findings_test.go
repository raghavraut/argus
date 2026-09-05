package state

import (
	"context"
	"path/filepath"
	"testing"
)

func TestFindingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	rows := []StoredFinding{
		{Asset: "a.t", TemplateID: "exposed-git-config", Severity: "high", Name: "Git", Matched: "https://a.t/.git/config", URL: "https://a.t/.git/config"},
		{Asset: "a.t", TemplateID: "tech-detect", Severity: "info", Name: "Tech", Matched: "https://a.t/", URL: "https://a.t/"},
	}
	if err := s.SaveFindings(ctx, "c1", rows); err != nil {
		t.Fatal(err)
	}
	// Rerun-safe: same rows ignored, no duplicates.
	if err := s.SaveFindings(ctx, "c1", rows); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadFindings(ctx, "c1", "a.t")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d findings", len(got))
	}
	counts, err := s.FindingCounts(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if counts["a.t"] != 2 {
		t.Fatalf("counts=%v", counts)
	}
	if n, err := s.ResetCampaign(ctx, "c1"); err != nil || n == 0 {
		t.Fatalf("reset n=%d err=%v", n, err)
	}
	got, _ = s.LoadFindings(ctx, "c1", "")
	if len(got) != 0 {
		t.Fatalf("reset left %d findings", len(got))
	}
}
