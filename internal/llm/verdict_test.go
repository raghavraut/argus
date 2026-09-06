package llm

import (
	"strings"
	"testing"
	"time"

	"github.com/raghavraut/rarefy/internal/core"
)

func baseResult(score float64) core.TriageResult {
	return core.TriageResult{
		Asset: "a.t", FinalScore: score, Confidence: 0.5,
		Recommendation: "manual_review_medium",
	}
}

func TestApplyPromote(t *testing.T) {
	for _, class := range []string{"UNIQUE_APP", "ADMIN_PANEL", "unique_app"} {
		got := ApplyVerdict(baseResult(0.5), core.LLMClassification{
			Classification: class, Confidence: 0.8, Reason: "custom login",
		})
		want := 0.5 + 0.2*0.8 // 0.66 → crosses into high priority
		if got.FinalScore != want {
			t.Fatalf("%s: score=%.3f want %.3f", class, got.FinalScore, want)
		}
		if got.Recommendation != "manual_review_high_priority" {
			t.Fatalf("%s: rec=%q", class, got.Recommendation)
		}
	}
}

func TestApplyDemote(t *testing.T) {
	for _, class := range []string{"WAF_BLOCK", "PARKED", "GENERIC_ERROR"} {
		got := ApplyVerdict(baseResult(0.4), core.LLMClassification{
			Classification: class, Confidence: 0.9, Reason: "cloudflare",
		})
		if got.FinalScore != 0.2 {
			t.Fatalf("%s: score=%.3f want 0.2", class, got.FinalScore)
		}
	}
}

func TestApplyIgnored(t *testing.T) {
	// Degraded, low confidence, and UNKNOWN must not move the score.
	cases := []core.LLMClassification{
		{Classification: "UNIQUE_APP", Confidence: 0.8, Degraded: true},
		{Classification: "UNIQUE_APP", Confidence: 0.5},
		{Classification: "UNKNOWN", Confidence: 0.95},
	}
	for _, c := range cases {
		if got := ApplyVerdict(baseResult(0.4), c); got.FinalScore != 0.4 {
			t.Fatalf("%+v moved score to %.3f", c, got.FinalScore)
		}
	}
}

func TestPromptTaxonomy(t *testing.T) {
	if !strings.Contains(systemPrompt, "WAF_BLOCK") ||
		!strings.Contains(systemPrompt, "PARKED") ||
		!strings.Contains(systemPrompt, "GENERIC_ERROR") ||
		!strings.Contains(systemPrompt, "UNIQUE_APP") ||
		!strings.Contains(systemPrompt, "ADMIN_PANEL") ||
		!strings.Contains(systemPrompt, "strict JSON") {
		t.Fatalf("system prompt lost the closed taxonomy:\n%s", systemPrompt)
	}
	// classify against an unreachable server degrades, never errors.
	c := NewClient("http://127.0.0.1:1", "none", 1, 2*time.Second)
	got, err := c.ClassifyAmbiguous(t.Context(), core.HTTPResponse{Asset: "a.t"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Degraded || got.Classification != "UNKNOWN" {
		t.Fatalf("expected degraded UNKNOWN, got %+v", got)
	}
}
