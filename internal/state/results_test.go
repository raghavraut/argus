package state

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/argus/argus/internal/core"
)

func seedResults(t *testing.T, s *Store) {
	t.Helper()
	rows := []ResultRow{
		{Result: core.TriageResult{Asset: "admin.t", FinalScore: 0.85, Confidence: 0.95, Recommendation: "manual_review_high_priority"},
			Status: 200, Title: "Admin Login", Tech: []string{"Jenkins"}, Headers: map[string]string{"server": "jetty"}},
		{Result: core.TriageResult{Asset: "api.t", FinalScore: 0.4, Confidence: 0.5, Recommendation: "manual_review_medium"},
			Status: 200, Title: "API", Tech: []string{"Express"}, Headers: map[string]string{}},
		{Result: core.TriageResult{Asset: "waf.t", FinalScore: 0.02, Confidence: 0.5, Recommendation: "ignore"},
			Status: 403, Title: "Forbidden", Tech: []string{"Cloudflare"}, Headers: map[string]string{}},
	}
	if err := s.SaveResults(context.Background(), "c1", rows); err != nil {
		t.Fatal(err)
	}
}

func TestFilterScores(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	seedResults(t, s)

	got, err := s.QueryResults(ctx, "c1", FilterParams{MinScore: 0.6})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Result.Asset != "admin.t" {
		t.Fatalf("min-score got %+v", got)
	}
	// Headers round-trip for the side panel.
	if got[0].Headers["server"] != "jetty" {
		t.Fatalf("headers lost: %+v", got[0].Headers)
	}
}

func TestFilterTechCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	seedResults(t, s)

	got, err := s.QueryResults(ctx, "c1", FilterParams{Tech: []string{"jenkins"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Result.Asset != "admin.t" {
		t.Fatalf("tech filter got %+v", got)
	}
	// OR semantics across multiple techs.
	got, err = s.QueryResults(ctx, "c1", FilterParams{Tech: []string{"jenkins", "express"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("tech OR got %d rows", len(got))
	}
	// Injection attempt is data, not SQL: matches nothing, breaks nothing.
	got, err = s.QueryResults(ctx, "c1", FilterParams{Tech: []string{`x" OR "1"="1`}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("injection matched %d rows", len(got))
	}
}

func TestFilterLimitAndOrder(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	seedResults(t, s)

	got, err := s.QueryResults(ctx, "c1", FilterParams{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Result.Asset != "admin.t" || got[1].Result.Asset != "api.t" {
		t.Fatalf("order/limit wrong: %+v", got)
	}
}
