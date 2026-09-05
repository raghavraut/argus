// Package llm — verdict.go: deterministic mapping from model verdicts to scores.
//
// Extracted as a pure function so the promotion/demotion policy is unit
// tested without an Ollama server. scan.go is the only caller.
package llm

import (
	"strings"

	"github.com/argus/argus/internal/core"
)

// Verdict confidence floor: below this the model is guessing, ignore it.
const minVerdictConfidence = 0.7

// promotionBoost scales high-signal verdicts into the score.
const promotionBoost = 0.2

// ApplyVerdict maps one LLM classification onto a triage result.
// Taxonomy (case-insensitive):
//
//	UNIQUE_APP, ADMIN_PANEL                    → promote (+0.2×confidence, cap 1)
//	WAF_BLOCK, PARKED, GENERIC_ERROR           → demote (×0.5)
//	UNKNOWN or degraded or low confidence      → unchanged
func ApplyVerdict(res core.TriageResult, v core.LLMClassification) core.TriageResult {
	if v.Degraded || v.Confidence < minVerdictConfidence {
		return res
	}
	switch strings.ToUpper(strings.TrimSpace(v.Classification)) {
	case "UNIQUE_APP", "ADMIN_PANEL":
		res.FinalScore += promotionBoost * v.Confidence
		if res.FinalScore > 1 {
			res.FinalScore = 1
		}
		res.Evidence = append(res.Evidence, "llm:"+v.Classification+" "+v.Reason)
		if res.FinalScore >= 0.6 {
			res.Recommendation = "manual_review_high_priority"
		}
	case "WAF_BLOCK", "PARKED", "GENERIC_ERROR":
		res.FinalScore *= 0.5
		res.Evidence = append(res.Evidence, "llm:"+v.Classification+" "+v.Reason)
	default:
		// UNKNOWN or novel label: record but do not move the score.
		res.Evidence = append(res.Evidence, "llm:"+v.Classification+" "+v.Reason)
	}
	return res
}
