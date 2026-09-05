package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raghavraut/argus/internal/core"
	"github.com/raghavraut/argus/internal/state"
)

func seed(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	s, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	nodes := []core.Node{
		{ID: "a.t", Type: core.NodeAsset, Score: 0.8, Attrs: map[string]string{"title": "Shop"}},
		{ID: "b.t", Type: core.NodeAsset, Score: 0.3},
	}
	edges := []core.Edge{
		{From: "a.t", To: "b.t", Type: core.EdgeRedirectsTo},
		{From: "b.t", To: "a.t", Type: core.EdgeRedirectsTo},
	}
	if err := s.SaveGraph(ctx, "c1", nodes, edges); err != nil {
		t.Fatal(err)
	}
	rows := []state.ResultRow{
		{Result: core.TriageResult{Asset: "a.t", FinalScore: 0.8, Confidence: 0.9, Evidence: []string{"admin+login"}, Recommendation: "manual_review_high_priority", Final: true},
			Status: 200, Title: "Shop", Tech: []string{"Shopify"}, Headers: map[string]string{"server": "nginx"}},
		{Result: core.TriageResult{Asset: "b.t", FinalScore: 0.3, Confidence: 0.5, Recommendation: "manual_review_medium", Final: true},
			Status: 200, Title: "Blog", Tech: []string{"WordPress"}},
	}
	if err := s.SaveResults(ctx, "c1", rows); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveFindings(ctx, "c1", []state.StoredFinding{
		{Asset: "a.t", TemplateID: "exposed-git-config", Severity: "high", Name: "Git", Matched: "https://a.t/.git/config", URL: "https://a.t/.git/config"},
	}); err != nil {
		t.Fatal(err)
	}
	s.MarkDone("c1", "a.t", "probe")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
}

func newTestServer(t *testing.T) (*Server, *state.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ui.db")
	seed(t, path)
	store, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv, err := New(store, "c1")
	if err != nil {
		t.Fatal(err)
	}
	return srv, store
}

func TestIndexRenders(t *testing.T) {
	srv, _ := newTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("index code=%d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"ARGUS", "view-graph", "view-corpus", "mermaid.min.js", "/static/app.js"} {
		if !strings.Contains(body, want) {
			t.Fatalf("index missing %q", want)
		}
	}
}

func TestStaticEmbedded(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, p := range []string{"/static/app.js", "/static/style.css"} {
		r := httptest.NewRequest(http.MethodGet, p, nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Body.Len() == 0 {
			t.Fatalf("%s code=%d len=%d", p, w.Code, w.Body.Len())
		}
	}
}

func TestGraphAPIHasClicks(t *testing.T) {
	srv, _ := newTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/api/graph", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	var g struct {
		Mermaid string            `json:"mermaid"`
		Index   map[string]string `json:"index"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(g.Mermaid, "graph LR\n") || !strings.Contains(g.Mermaid, "click n0 onNodeClick") {
		t.Fatalf("no click bindings:\n%s", g.Mermaid)
	}
	if len(g.Index) != 2 {
		t.Fatalf("index=%v", g.Index)
	}
}

func TestAssetsAPIBand(t *testing.T) {
	srv, _ := newTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/api/assets", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%d", len(rows))
	}
	bands := map[string]bool{}
	for _, row := range rows {
		bands[row["asset"].(string)] = row["in_band"].(bool)
	}
	// 0.8 is above the band, 0.3 is inside [0.15, 0.6).
	if bands["a.t"] || !bands["b.t"] {
		t.Fatalf("bands=%v", bands)
	}
}

func TestAssetDetailAnd404(t *testing.T) {
	srv, _ := newTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/api/asset?id=a.t", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	var d struct {
		Headers  map[string]string    `json:"headers"`
		Tech     []string             `json:"tech"`
		Findings []state.StoredFinding `json:"findings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Headers["server"] != "nginx" || len(d.Tech) != 1 {
		t.Fatalf("detail=%+v", d)
	}
	if len(d.Findings) != 1 || d.Findings[0].TemplateID != "exposed-git-config" {
		t.Fatalf("findings=%+v", d.Findings)
	}

	r = httptest.NewRequest(http.MethodGet, "/api/assets", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	counts := map[string]float64{}
	for _, row := range rows {
		counts[row["asset"].(string)] = row["finding_count"].(float64)
	}
	if counts["a.t"] != 1 || counts["b.t"] != 0 {
		t.Fatalf("finding counts=%v", counts)
	}

	r = httptest.NewRequest(http.MethodGet, "/api/asset?id=nope", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("code=%d", w.Code)
	}
}
