// Package ui serves the local triage dashboard.
//
// Single-binary constraint: templates and static assets are embedded with
// go:embed, so `rarefy ui` needs no extra files. The frontend is vanilla
// JS/CSS (no build step); only mermaid.js loads from CDN, with a graceful
// fallback to the corpus table when offline.
package ui

import (
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"strings"

	"github.com/raghavraut/rarefy/internal/export"
	"github.com/raghavraut/rarefy/internal/state"
	"github.com/raghavraut/rarefy/internal/triage"
)

//go:embed templates/* static/*
var content embed.FS

// Server renders one campaign from the SQLite store.
type Server struct {
	store   *state.Store
	defCamp string
	mux     *http.ServeMux
	tpl     *template.Template
}

// New binds routes. defCamp may be "" (falls back to latest per request).
func New(store *state.Store, defCamp string) (*Server, error) {
	tpl, err := template.ParseFS(content, "templates/index.html")
	if err != nil {
		return nil, err
	}
	staticFS, err := fs.Sub(content, "static")
	if err != nil {
		return nil, err
	}
	s := &Server{store: store, defCamp: defCamp, mux: http.NewServeMux(), tpl: tpl}
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	s.mux.HandleFunc("GET /", s.handleIndex)
	s.mux.HandleFunc("GET /api/campaigns", s.handleCampaigns)
	s.mux.HandleFunc("GET /api/info", s.handleInfo)
	s.mux.HandleFunc("GET /api/graph", s.handleGraph)
	s.mux.HandleFunc("GET /api/assets", s.handleAssets)
	s.mux.HandleFunc("GET /api/asset", s.handleAsset)
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// resolveCampaign prefers ?campaign=, then the server default, then latest.
func (s *Server) resolveCampaign(r *http.Request) (string, bool) {
	if c := strings.TrimSpace(r.URL.Query().Get("campaign")); c != "" {
		return c, true
	}
	if s.defCamp != "" {
		return s.defCamp, true
	}
	c, err := s.store.LatestCampaign(r.Context())
	if err != nil || c == "" {
		return "", false
	}
	return c, true
}

type indexData struct {
	Campaign     string
	CampaignJSON template.JS
	BandLo       float64
	BandHi       float64
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	camp, ok := s.resolveCampaign(r)
	if !ok {
		http.Error(w, "no campaigns: run `rarefy scan` first", http.StatusNotFound)
		return
	}
	cj, _ := json.Marshal(camp)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.tpl.ExecuteTemplate(w, "index.html", indexData{
		Campaign: camp, CampaignJSON: template.JS(cj),
		BandLo: triage.AmbiguityLo, BandHi: triage.AmbiguityHi,
	})
}

func (s *Server) handleCampaigns(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.ListCampaigns(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if stats == nil {
		stats = []state.CampaignStats{}
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	camp, ok := s.resolveCampaign(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "no campaigns")
		return
	}
	nodes, edges, err := s.store.LoadGraph(r.Context(), camp)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	n, err := s.store.ResultCount(r.Context(), camp)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"campaign": camp, "nodes": len(nodes), "edges": len(edges), "results": n,
	})
}

func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	camp, ok := s.resolveCampaign(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "no campaigns")
		return
	}
	nodes, edges, err := s.store.LoadGraph(r.Context(), camp)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if r.URL.Query().Get("format") == "dot" {
		w.Header().Set("Content-Type", "text/vnd.graphviz")
		_ = export.WriteDOT(w, nodes, edges, export.Options{MaxNodes: 300})
		return
	}
	src, index := export.IndexedMermaid(nodes, edges, export.Options{MaxNodes: 300}, "onNodeClick")
	writeJSON(w, http.StatusOK, map[string]any{"mermaid": src, "index": index})
}

type assetView struct {
	Asset          string   `json:"asset"`
	Score          float64  `json:"score"`
	Confidence     float64  `json:"confidence"`
	Rarity         float64  `json:"rarity"`
	Status         int      `json:"status"`
	Title          string   `json:"title"`
	Tech           []string `json:"tech"`
	ClusterID      string   `json:"cluster_id"`
	Recommendation string   `json:"recommendation"`
	InBand         bool     `json:"in_band"`
	FindingCount   int      `json:"finding_count"`
}

func toView(r state.ResultRow, findings int) assetView {
	return assetView{
		Asset: r.Result.Asset, Score: r.Result.FinalScore,
		Confidence: r.Result.Confidence, Rarity: r.Result.RarityIndex,
		Status: r.Status, Title: r.Title, Tech: r.Tech,
		ClusterID: r.Result.ClusterID, Recommendation: r.Result.Recommendation,
		InBand: triage.InAmbiguityBand(r.Result.FinalScore),
		FindingCount: findings,
	}
}

func (s *Server) handleAssets(w http.ResponseWriter, r *http.Request) {
	camp, ok := s.resolveCampaign(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "no campaigns")
		return
	}
	rows, err := s.store.QueryResults(r.Context(), camp, state.FilterParams{})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	counts, err := s.store.FindingCounts(r.Context(), camp)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]assetView, 0, len(rows))
	for _, row := range rows {
		out = append(out, toView(row, counts[row.Result.Asset]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "missing ?id=")
		return
	}
	camp, ok := s.resolveCampaign(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "no campaigns")
		return
	}
	rows, err := s.store.QueryResults(r.Context(), camp, state.FilterParams{})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, row := range rows {
		if row.Result.Asset == id {
			findings, err := s.store.LoadFindings(r.Context(), camp, id)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			if findings == nil {
				findings = []state.StoredFinding{}
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"result": row.Result, "status": row.Status,
				"title": row.Title, "tech": row.Tech, "headers": row.Headers,
				"findings": findings,
			})
			return
		}
	}
	writeErr(w, http.StatusNotFound, "unknown asset")
}
