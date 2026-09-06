package state

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/raghavraut/rarefy/internal/core"
)

func TestGraphRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	nodes := []core.Node{
		{ID: "a.t", Type: core.NodeAsset, Score: 0.9, Attrs: map[string]string{"title": "Shop"}},
		{ID: "ip:1.2.3.4", Type: core.NodeInfrastructure},
	}
	edges := []core.Edge{
		{From: "a.t", To: "ip:1.2.3.4", Type: core.EdgeResolvesTo},
		{From: "ip:1.2.3.4", To: "a.t", Type: core.EdgeLinkedFrom}, // cycle ok
	}
	if err := s.SaveGraph(ctx, "c1", nodes, edges); err != nil {
		t.Fatal(err)
	}
	// Idempotent overwrite, not duplication.
	if err := s.SaveGraph(ctx, "c1", nodes, edges); err != nil {
		t.Fatal(err)
	}
	ln, le, err := s.LoadGraph(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ln) != 2 || len(le) != 2 {
		t.Fatalf("got %d nodes %d edges, want 2/2", len(ln), len(le))
	}
	if ln[0].Attrs["title"] != "Shop" && ln[1].Attrs["title"] != "Shop" {
		t.Fatalf("attrs lost: %+v", ln)
	}
	stats, err := s.ListCampaigns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].Nodes != 2 || stats[0].Edges != 2 {
		t.Fatalf("bad stats: %+v", stats)
	}
	if n, err := s.ResetCampaign(ctx, "c1"); err != nil || n == 0 {
		t.Fatalf("reset n=%d err=%v", n, err)
	}
	ln, _, _ = s.LoadGraph(ctx, "c1")
	if len(ln) != 0 {
		t.Fatalf("reset left %d nodes", len(ln))
	}
}
