// Package triage — corpus.go: live-fire tuning dumps.
//
// The corpus dumper serializes everything needed to tune the scorer
// offline: per-asset token sketches, the document-frequency table, the
// derived IDF weights, and per-asset scores flagged by ambiguity band.
// This keeps analysts out of the raw JSONL stream.
package triage

import (
	"context"
	"encoding/json"
	"io"
	"sort"

	"github.com/raghavraut/rarefy/internal/core"
)

// DocDump is one asset's tuning record.
type DocDump struct {
	Asset           string         `json:"asset"`
	Status          int            `json:"status"`
	Title           string         `json:"title"`
	Rarity          float64        `json:"rarity"`
	Score           float64        `json:"score"`
	Confidence      float64        `json:"confidence"`
	InAmbiguityBand bool           `json:"in_ambiguity_band"`
	ClusterID       string         `json:"cluster_id"`
	Tokens          map[string]int `json:"tokens"`
	TotalTokens     int            `json:"total_tokens"`
}

// CorpusDump is the full --export-corpus payload.
type CorpusDump struct {
	Campaign string             `json:"campaign"`
	NumDocs  int                `json:"num_docs"`
	DF       map[string]int     `json:"df"`
	Weights  map[string]float64 `json:"weights"`
	Docs     []DocDump          `json:"docs"`
}

// DumpCorpus builds the tuning payload from the engine plus final results.
// results maps asset name -> scored TriageResult (post-LLM adjustments).
func (e *Engine) DumpCorpus(campaign string, results map[string]core.TriageResult) CorpusDump {
	d := CorpusDump{
		Campaign: campaign,
		NumDocs:  e.numDocs,
		DF:       e.df,
		Weights:  e.weights,
	}
	names := make([]string, 0, len(e.byAsset))
	for name := range e.byAsset {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		doc := e.byAsset[name]
		rec := DocDump{
			Asset:       name,
			Status:      doc.StatusCode,
			Title:       doc.Title,
			Rarity:      round3(RarityIndex(doc, e.weights)),
			Tokens:      doc.TokenCounts,
			TotalTokens: doc.TotalTokens,
			ClusterID:   e.clusterOf[name],
		}
		if r, ok := results[name]; ok {
			rec.Score = r.FinalScore
			rec.Confidence = r.Confidence
			rec.InAmbiguityBand = InAmbiguityBand(r.FinalScore)
		}
		d.Docs = append(d.Docs, rec)
	}
	return d
}

// WriteCorpusDump encodes the dump as indented JSON.
func WriteCorpusDump(_ context.Context, w io.Writer, dump CorpusDump) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(dump)
}
