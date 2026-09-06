package triage

import (
	"context"
	"strconv"
	"testing"

	"github.com/raghavraut/rarefy/internal/core"
)

// syntheticCorpus builds n deterministic docs with realistic overlap:
// a few shared boilerplate tokens plus per-doc unique tokens, mixed
// statuses/titles so clustering has real work to do.
func syntheticCorpus(n int) []core.HTTPResponse {
	titles := []string{"Shop", "Admin Login", "Forbidden", "API Docs", "Blog"}
	out := make([]core.HTTPResponse, 0, n)
	for i := 0; i < n; i++ {
		status := 200
		switch i % 5 {
		case 1:
			status = 403
		case 2:
			status = 404
		case 3:
			status = 301
		}
		r := core.HTTPResponse{
			Asset:       "host" + strconv.Itoa(i) + ".t",
			StatusCode:  status,
			Title:       titles[i%len(titles)],
			Headers:     map[string]string{"server": "cloudflare", "x-id": strconv.Itoa(i)},
			BodyPreview: "common boilerplate html head body unique-token-" + strconv.Itoa(i),
			FaviconHash: "fav" + strconv.Itoa(i%7),
		}
		r.TokenCounts = Tokenize(r)
		t := 0
		for _, c := range r.TokenCounts {
			t += c
		}
		r.TotalTokens = t
		r.SimHash = SimHash64(r.Title + " " + r.BodyPreview)
		out = append(out, r)
	}
	return out
}

// BenchmarkScore measures per-asset scoring cost (the DAG hot path:
// called once per asset). Pre-fix this was O(N) per call (full-corpus
// min/max rescan); post-fix it must be O(1).
func BenchmarkScore(b *testing.B) {
	corpus := syntheticCorpus(3000)
	e, err := NewEngine(context.Background(), corpus)
	if err != nil {
		b.Fatal(err)
	}
	names := make([]string, 0, len(corpus))
	for _, d := range corpus {
		names = append(names, d.Asset)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.Score(context.Background(), core.Asset{Name: names[i%len(names)]}, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkNewEngine measures one-time construction (TF-IDF + clustering).
func BenchmarkNewEngine(b *testing.B) {
	corpus := syntheticCorpus(3000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := NewEngine(context.Background(), corpus); err != nil {
			b.Fatal(err)
		}
	}
}
