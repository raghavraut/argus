package triage

import (
	"context"
	"testing"

	"github.com/raghavraut/rarefy/internal/core"
)

func TestDumpCorpusBands(t *testing.T) {
	ctx := context.Background()
	corpus := []core.HTTPResponse{
		mkResp("boring.t", 403, "Forbidden", "forbidden waf block", map[string]string{"server": "cloudflare"}),
		mkResp("boring2.t", 403, "Forbidden", "forbidden waf block", map[string]string{"server": "cloudflare"}),
		mkResp("admin.t", 200, "Admin Login", "login password admin panel", nil),
	}
	e, err := NewEngine(ctx, corpus)
	if err != nil {
		t.Fatal(err)
	}
	if e.NumDocs() != 3 {
		t.Fatalf("numDocs=%d, want 3", e.NumDocs())
	}
	if len(e.DF()) == 0 || len(e.Weights()) == 0 {
		t.Fatal("df/weights must be non-empty for tuning")
	}
	results := map[string]core.TriageResult{}
	for _, d := range corpus {
		r, err := e.Score(ctx, core.Asset{Name: d.Asset}, nil)
		if err != nil {
			t.Fatal(err)
		}
		results[d.Asset] = r
	}
	dump := e.DumpCorpus("livefire", results)
	if dump.Campaign != "livefire" || dump.NumDocs != 3 || len(dump.Docs) != 3 {
		t.Fatalf("bad dump envelope: %+v", dump.Campaign)
	}
	for _, doc := range dump.Docs {
		if doc.Tokens == nil || doc.TotalTokens == 0 {
			t.Fatalf("doc %s missing sketch", doc.Asset)
		}
		wantBand := InAmbiguityBand(doc.Score)
		if doc.InAmbiguityBand != wantBand {
			t.Fatalf("doc %s band=%v score=%.3f", doc.Asset, doc.InAmbiguityBand, doc.Score)
		}
	}
}
