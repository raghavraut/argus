package export

import (
	"strings"
	"testing"

	"github.com/raghavraut/argus/internal/core"
)

func cyclicGraph() ([]core.Node, []core.Edge) {
	nodes := []core.Node{
		{ID: "a.target.com", Type: core.NodeAsset, Score: 0.8, Attrs: map[string]string{"title": "Shop"}},
		{ID: "b.target.com", Type: core.NodeAsset, Score: 0.2},
		{ID: "ip:1.2.3.4", Type: core.NodeInfrastructure},
		{ID: "cert:shared", Type: core.NodeIdentity},
	}
	edges := []core.Edge{
		// redirect loop a->b->a must not break the renderer
		{From: "a.target.com", To: "b.target.com", Type: core.EdgeRedirectsTo},
		{From: "b.target.com", To: "a.target.com", Type: core.EdgeRedirectsTo},
		{From: "a.target.com", To: "b.target.com", Type: core.EdgeRedirectsTo}, // dup
		{From: "a.target.com", To: "ip:1.2.3.4", Type: core.EdgeResolvesTo},
		{From: "b.target.com", To: "ip:1.2.3.4", Type: core.EdgeResolvesTo},
		{From: "a.target.com", To: "cert:shared", Type: core.EdgeSharesCert},
		{From: "ghost", To: "nowhere", Type: core.EdgeLinkedFrom}, // pruned by cap/keep
	}
	return nodes, edges
}

func TestDOTCycleAndDedupe(t *testing.T) {
	nodes, edges := cyclicGraph()
	var b strings.Builder
	if err := WriteDOT(&b, nodes, edges, Options{}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.HasPrefix(out, "digraph argus {") || !strings.HasSuffix(out, "}\n") {
		t.Fatalf("bad DOT envelope:\n%s", out)
	}
	// Duplicate redirect edge must render exactly once per direction.
	if got := strings.Count(out, `"a.target.com" -> "b.target.com"`); got != 1 {
		t.Fatalf("expected 1 deduped a->b edge, got %d:\n%s", got, out)
	}
	if got := strings.Count(out, `"b.target.com" -> "a.target.com"`); got != 1 {
		t.Fatalf("expected cycle back-edge b->a, got %d:\n%s", got, out)
	}
	// IDs with colons/dots must be quoted so Graphviz parses them.
	if !strings.Contains(out, `"ip:1.2.3.4"`) {
		t.Fatalf("unquoted infra id:\n%s", out)
	}
	// High-score asset colored red, low-score gray.
	if !strings.Contains(out, "fillcolor=red") || !strings.Contains(out, "fillcolor=gray") {
		t.Fatalf("missing score colors:\n%s", out)
	}
	// Pruned edge (nodes outside snapshot) must not render.
	if strings.Contains(out, "ghost") {
		t.Fatalf("leaked pruned edge:\n%s", out)
	}
}

func TestMermaidCycle(t *testing.T) {
	nodes, edges := cyclicGraph()
	var b strings.Builder
	if err := WriteMermaid(&b, nodes, edges, Options{}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.HasPrefix(out, "graph LR\n") {
		t.Fatalf("bad mermaid header:\n%s", out)
	}
	// Raw IDs contain dots/colons — they must appear only inside bracket
	// labels, never as bare Mermaid node tokens or edge endpoints.
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == "graph LR" {
			continue
		}
		if strings.Contains(trimmed, "-->") {
			for _, raw := range []string{"a.target.com", "b.target.com", "ip:1.2.3.4", "cert:shared"} {
				if strings.Contains(trimmed, raw) {
					t.Fatalf("raw id %q in edge line %q:\n%s", raw, trimmed, out)
				}
			}
		} else if !(strings.HasPrefix(trimmed, "n") && strings.Contains(trimmed, "[")) {
			t.Fatalf("node line without safe token id %q:\n%s", trimmed, out)
		}
	}
	// Both directions of the loop render (edge list is cycle-safe).
	if strings.Count(out, "REDIRECTS_TO") != 2 {
		t.Fatalf("expected both loop directions:\n%s", out)
	}
}

func TestMaxNodesCap(t *testing.T) {
	nodes, edges := cyclicGraph()
	var b strings.Builder
	if err := WriteDOT(&b, nodes, edges, Options{MaxNodes: 2}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	// Assets-first: both assets kept, infra/identity dropped with their edges.
	if !strings.Contains(out, `"a.target.com"`) || !strings.Contains(out, `"b.target.com"`) {
		t.Fatalf("assets must survive the cap:\n%s", out)
	}
	if strings.Contains(out, "ip:1.2.3.4") || strings.Contains(out, "cert:shared") {
		t.Fatalf("capped nodes must be pruned with their edges:\n%s", out)
	}
}

func TestUnknownFormat(t *testing.T) {
	nodes, edges := cyclicGraph()
	var b strings.Builder
	if err := Write(&b, "neo4j", nodes, edges, Options{}); err == nil {
		t.Fatal("expected error for unknown format")
	}
}
