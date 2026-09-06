package triage

import (
	"context"
	"testing"

	"github.com/raghavraut/rarefy/internal/core"
)

func mkResp(asset string, status int, title, body string, headers map[string]string) core.HTTPResponse {
	r := core.HTTPResponse{Asset: asset, StatusCode: status, Title: title, Headers: headers, BodyPreview: body}
	r.TokenCounts = Tokenize(r)
	t := 0
	for _, c := range r.TokenCounts {
		t += c
	}
	r.TotalTokens = t
	r.SimHash = SimHash64(title + " " + body)
	return r
}

// WAF trap: a 403/status token on 90% of corpus must weigh ~crushed vs a rare debug token.
func TestWAFSuppression(t *testing.T) {
	ctx := context.Background()
	var corpus []core.HTTPResponse
	for i := 0; i < 9; i++ {
		corpus = append(corpus, mkResp(
			"waf"+string(rune('0'+i))+".t", 403, "Forbidden",
			"forbidden waf block", map[string]string{"server": "cloudflare"},
		))
	}
	corpus = append(corpus, mkResp("rare.t", 200, "Debug Console",
		"x-debug-token console", map[string]string{"x-debug-token": "abc123"}))
	w, err := CalculateTFIDF(ctx, corpus)
	if err != nil {
		t.Fatal(err)
	}
	wafW := w["status:4xx"]
	dbgW := w["body:x-debug-token"]
	if !(dbgW > wafW*3) {
		t.Fatalf("expected rare debug weight (%.3f) >> waf weight (%.3f)", dbgW, wafW)
	}
}

// SameCloudflare favicon but different title/status must NOT cluster together.
func TestNoOverCluster(t *testing.T) {
	a := mkResp("a.t", 200, "Shop", "shop home", nil)
	a.FaviconHash = "cf-default"
	b := mkResp("b.t", 403, "Forbidden", "waf block", nil)
	b.FaviconHash = "cf-default"
	if SameCluster(a, b) {
		t.Fatal("distinct apps behind same favicon must not cluster")
	}
	c := mkResp("a2.t", 200, "Shop", "shop home", nil)
	c.FaviconHash = "cf-default"
	c.SimHash = a.SimHash
	if !SameCluster(a, c) {
		t.Fatal("near-duplicates should cluster")
	}
}

func TestEngineScoreOrdering(t *testing.T) {
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
	rb, _ := e.Score(ctx, core.Asset{Name: "boring.t"}, nil)
	ra, _ := e.Score(ctx, core.Asset{Name: "admin.t"}, nil)
	if !(ra.FinalScore > rb.FinalScore) {
		t.Fatalf("admin (%.3f) should outscore waf (%0.3f)", ra.FinalScore, rb.FinalScore)
	}
	if !ra.Final {
		t.Fatal("Score must set Final=true")
	}
}
