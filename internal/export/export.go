// Package export renders EvidenceGraph snapshots for humans.
//
// Cycle safety: both formats are edge lists, so redirect loops (a→b→a)
// are representable by construction. The exporter additionally dedupes
// edges, quotes/sanitizes every identifier (Graphviz chokes on raw
// "ip:1.2.3.4"-style IDs), and caps output via maxNodes so a 5k-host
// campaign cannot produce an unrenderable hairball.
package export

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/raghavraut/argus/internal/core"
)

// Format selects the renderer.
type Format string

const (
	FormatDOT     Format = "dot"
	FormatMermaid Format = "mermaid"
)

// Options bounds the render.
type Options struct {
	MaxNodes int // 0 = unlimited
}

// nodeRank orders node declarations deterministically by type then id.
func nodeRank(t core.NodeType) int {
	switch t {
	case core.NodeAsset:
		return 0
	case core.NodeInfrastructure:
		return 1
	case core.NodeIdentity:
		return 2
	case core.NodeSurface:
		return 3
	default:
		return 4
	}
}

// selectNodes caps and deterministically orders nodes: assets first
// (highest score first), then the rest by id.
func selectNodes(nodes []core.Node, max int) []core.Node {
	cp := append([]core.Node(nil), nodes...)
	sort.Slice(cp, func(i, j int) bool {
		if ri, rj := nodeRank(cp[i].Type), nodeRank(cp[j].Type); ri != rj {
			return ri < rj
		}
		if cp[i].Score != cp[j].Score {
			return cp[i].Score > cp[j].Score
		}
		return cp[i].ID < cp[j].ID
	})
	if max > 0 && len(cp) > max {
		cp = cp[:max]
	}
	return cp
}

// dedupeEdges drops exact-duplicate edges and any edge touching a node
// outside keep. Output order is deterministic.
func dedupeEdges(edges []core.Edge, keep map[string]bool) []core.Edge {
	seen := map[core.Edge]bool{}
	out := []core.Edge{}
	for _, e := range edges {
		if !keep[e.From] || !keep[e.To] {
			continue
		}
		if seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].To != out[j].To {
			return out[i].To < out[j].To
		}
		return out[i].Type < out[j].Type
	})
	return out
}

func dotQuote(s string) string {
	r := strings.ReplaceAll(s, `\`, `\\`)
	r = strings.ReplaceAll(r, `"`, `\"`)
	return `"` + r + `"`
}

func shapeFor(t core.NodeType) string {
	switch t {
	case core.NodeAsset:
		return "box"
	case core.NodeInfrastructure:
		return "ellipse"
	case core.NodeIdentity:
		return "diamond"
	case core.NodeSurface:
		return "note"
	default:
		return "box"
	}
}

func colorFor(score float64) string {
	switch {
	case score >= 0.6:
		return "red"
	case score >= 0.3:
		return "orange"
	default:
		return "gray"
	}
}

// WriteDOT renders Graphviz DOT: `dot -Tpng graph.dot -o surface.png`.
func WriteDOT(w io.Writer, nodes []core.Node, edges []core.Edge, opts Options) error {
	sel := selectNodes(nodes, opts.MaxNodes)
	keep := map[string]bool{}
	for _, n := range sel {
		keep[n.ID] = true
	}
	if _, err := io.WriteString(w, "digraph argus {\n  rankdir=LR;\n  node [style=filled];\n"); err != nil {
		return err
	}
	for _, n := range sel {
		label := n.ID
		if title, ok := n.Attrs["title"]; ok && title != "" {
			label += "\n" + title
		}
		line := fmt.Sprintf("  %s [label=%s shape=%s fillcolor=%s];\n",
			dotQuote(n.ID), dotQuote(label), shapeFor(n.Type), colorFor(n.Score))
		if _, err := io.WriteString(w, line); err != nil {
			return err
		}
	}
	for _, e := range dedupeEdges(edges, keep) {
		line := fmt.Sprintf("  %s -> %s [label=%s];\n",
			dotQuote(e.From), dotQuote(e.To), dotQuote(string(e.Type)))
		if _, err := io.WriteString(w, line); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "}\n")
	return err
}

// mermaidID maps arbitrary node IDs to safe tokens (n0, n1, ...);
// Mermaid node syntax breaks on colons, dots, and slashes.
func mermaidID(idx int) string { return fmt.Sprintf("n%d", idx) }

func mermaidLabel(s string) string {
	r := strings.ReplaceAll(s, `"`, `'`)
	r = strings.ReplaceAll(r, "\n", " ")
	r = strings.ReplaceAll(r, "[", "(")
	r = strings.ReplaceAll(r, "]", ")")
	r = strings.ReplaceAll(r, "{", "(")
	r = strings.ReplaceAll(r, "}", ")")
	return r
}

// WriteMermaid renders a `graph LR` diagram pasteable into GitHub Markdown.
func WriteMermaid(w io.Writer, nodes []core.Node, edges []core.Edge, opts Options) error {
	src, _ := renderMermaid(nodes, edges, opts, "")
	_, err := io.WriteString(w, src)
	return err
}

// IndexedMermaid renders Mermaid plus the token→nodeID index and per-node
// `click` bindings invoking clickFn(token) in the page. Powers the UI
// side-panel: clicking a node opens its TriageResult.
func IndexedMermaid(nodes []core.Node, edges []core.Edge, opts Options, clickFn string) (string, map[string]string) {
	return renderMermaid(nodes, edges, opts, clickFn)
}

func renderMermaid(nodes []core.Node, edges []core.Edge, opts Options, clickFn string) (string, map[string]string) {
	sel := selectNodes(nodes, opts.MaxNodes)
	keep := map[string]bool{}
	ids := map[string]string{}
	for i, n := range sel {
		keep[n.ID] = true
		ids[n.ID] = mermaidID(i)
	}
	var b strings.Builder
	b.WriteString("graph LR\n")
	for _, n := range sel {
		label := mermaidLabel(n.ID)
		if title, ok := n.Attrs["title"]; ok && title != "" {
			label += " " + mermaidLabel(title)
		}
		fmt.Fprintf(&b, "  %s[%s]\n", ids[n.ID], label)
	}
	for _, e := range dedupeEdges(edges, keep) {
		fmt.Fprintf(&b, "  %s -->|%s| %s\n", ids[e.From], string(e.Type), ids[e.To])
	}
	if clickFn != "" {
		for _, n := range sel {
			fmt.Fprintf(&b, "  click %s %s\n", ids[n.ID], clickFn)
		}
	}
	// Invert to token→nodeID for the frontend lookup.
	index := make(map[string]string, len(ids))
	for nodeID, tok := range ids {
		index[tok] = nodeID
	}
	return b.String(), index
}

// Write dispatches on format name ("dot" or "mermaid", case-insensitive).
func Write(w io.Writer, format string, nodes []core.Node, edges []core.Edge, opts Options) error {
	switch Format(strings.ToLower(strings.TrimSpace(format))) {
	case FormatDOT:
		return WriteDOT(w, nodes, edges, opts)
	case FormatMermaid:
		return WriteMermaid(w, nodes, edges, opts)
	default:
		return fmt.Errorf("unknown format %q (want dot|mermaid)", format)
	}
}
