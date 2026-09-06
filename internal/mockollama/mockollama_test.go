package mockollama

import (
	"context"
	"testing"
	"time"

	"github.com/raghavraut/rarefy/internal/core"
	"github.com/raghavraut/rarefy/internal/llm"
)

// Happy path: the client parses mock taxonomy verdicts through the real
// /api/chat HTTP round-trip.
func TestMockTaxonomy(t *testing.T) {
	mock := New(t)
	client := llm.NewClient(mock.URL(), "mock-llm", 2, 5*time.Second)

	cases := []struct {
		resp  core.HTTPResponse
		class string
	}{
		{core.HTTPResponse{Asset: "w.t", StatusCode: 403, Title: "Forbidden"}, "WAF_BLOCK"},
		{core.HTTPResponse{Asset: "a.t", StatusCode: 200, Title: "Login"}, "ADMIN_PANEL"},
		{core.HTTPResponse{Asset: "u.t", StatusCode: 200, Title: "Shop"}, "UNIQUE_APP"},
	}
	for _, c := range cases {
		got, err := client.ClassifyAmbiguous(context.Background(), c.resp)
		if err != nil {
			t.Fatal(err)
		}
		if got.Degraded || got.Classification != c.class {
			t.Fatalf("%s: got %+v want %s", c.resp.Asset, got, c.class)
		}
		// Verdict must flow through the production multiplier policy.
		mapped := llm.ApplyVerdict(core.TriageResult{Asset: c.resp.Asset, FinalScore: 0.4}, got)
		if len(mapped.Evidence) == 0 {
			t.Fatalf("%s: verdict not applied", c.resp.Asset)
		}
	}
	if mock.Requests() != int64(len(cases)) {
		t.Fatalf("requests=%d want %d", mock.Requests(), len(cases))
	}
}

// Degradation path: dead server must yield UNKNOWN, never an error or hang.
func TestMockKilledDegrades(t *testing.T) {
	mock := New(t)
	url := mock.URL()
	mock.Close() // simulate Ollama down

	client := llm.NewClient(url, "mock-llm", 2, 5*time.Second)
	start := time.Now()
	got, err := client.ClassifyAmbiguous(context.Background(), core.HTTPResponse{Asset: "x.t"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Degraded || got.Classification != "UNKNOWN" {
		t.Fatalf("expected degraded UNKNOWN, got %+v", got)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatal("degradation must fail fast, not hang")
	}
}
