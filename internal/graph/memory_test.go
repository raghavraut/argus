package graph

import (
	"context"
	"sync"
	"testing"

	"github.com/raghavraut/rarefy/internal/core"
)

func TestPropagateTerminatesOnCycle(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryGraph()
	for _, id := range []string{"a", "b", "c"} {
		if err := g.AddNode(ctx, core.Node{ID: id, Type: core.NodeAsset}); err != nil {
			t.Fatal(err)
		}
	}
	// cycle a->b->c->a
	for _, e := range []core.Edge{
		{From: "a", To: "b", Type: core.EdgeRedirectsTo},
		{From: "b", To: "c", Type: core.EdgeRedirectsTo},
		{From: "c", To: "a", Type: core.EdgeRedirectsTo},
	} {
		if err := g.AddEdge(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.PropagateScore(ctx, "a", 1.0, 0.5); err != nil {
		t.Fatal(err)
	}
	nb, _ := g.GetNeighbors(ctx, "a", core.EdgeRedirectsTo)
	if len(nb) != 1 || nb[0].ID != "b" {
		t.Fatalf("expected neighbor b, got %+v", nb)
	}
}

func TestConcurrentAdd(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryGraph()
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := string(rune('a'+i%26)) + string(rune('0'+i%10)) + itoa(i)
			_ = g.AddNode(ctx, core.Node{ID: id, Type: core.NodeAsset})
			_ = g.AddEdge(ctx, core.Edge{From: id, To: "ip:1.2.3.4", Type: core.EdgeResolvesTo})
		}(i)
	}
	wg.Wait()
	nodes, edges, err := g.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) == 0 || len(edges) == 0 {
		t.Fatalf("expected nodes+edges, got %d nodes %d edges", len(nodes), len(edges))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	p := len(b)
	for n > 0 {
		p--
		b[p] = byte('0' + n%10)
		n /= 10
	}
	return string(b[p:])
}
