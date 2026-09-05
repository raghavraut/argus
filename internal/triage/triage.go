// Package triage implements campaign-aware rarity scoring.
//
// Memory fix: TF-IDF operates on per-response token sketches
// (TokenCounts + TotalTokens), never on full bodies. The corpus passed to
// CalculateTFIDF is a slice of lightweight sketches; callers must not
// retain response bodies beyond the probe stage.
package triage

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"hash/fnv"
	"math"
	"sort"
	"strings"

	"github.com/raghavraut/argus/internal/core"
)

const (
	// stopListThreshold suppresses boilerplate: a token present in more
	// than 70% of the corpus gets its weight crushed (WAF/CDN trap fix).
	stopListThreshold = 0.7
	stopListPenalty   = 0.1
)

// Tokenize extracts lowercase alphanumeric signals from title, headers, and
// body preview. It is intentionally small: headers keys, status bucket,
// title words, and rare header values carry the signal.
func Tokenize(resp core.HTTPResponse) map[string]int {
	counts := map[string]int{}
	add := func(tok string) {
		tok = strings.ToLower(strings.TrimSpace(tok))
		if len(tok) < 2 || len(tok) > 64 {
			return
		}
		counts[tok]++
	}
	for _, w := range strings.Fields(resp.Title) {
		add("title:" + w)
	}
	add(strings.ToLower("status:" + statusBucket(resp.StatusCode)))
	for k, v := range resp.Headers {
		kl := strings.ToLower(strings.TrimSpace(k))
		if kl == "" {
			continue
		}
		add("hdr:" + kl)
		// Rare header *values* (debug tokens, versions) are high-signal;
		// common values are suppressed later by IDF.
		for _, w := range strings.Fields(v) {
			if len(w) < 3 {
				continue
			}
			add("hdrval:" + kl + "=" + strings.ToLower(w))
		}
	}
	for _, w := range strings.Fields(resp.BodyPreview) {
		clean := strings.ToLower(strings.Trim(w, " \t\n\r\"'`<>(){}[]:;,./\\"))
		add("body:" + clean)
	}
	if resp.FaviconHash != "" {
		add("favicon:" + strings.ToLower(resp.FaviconHash))
	}
	return counts
}

func statusBucket(code int) string {
	switch {
	case code >= 200 && code < 300:
		return "2xx"
	case code >= 300 && code < 400:
		return "3xx"
	case code >= 400 && code < 500:
		return "4xx"
	case code >= 500:
		return "5xx"
	default:
		return "unknown"
	}
}

// CalculateTFIDF computes smoothed rarity weights per token.
//
// IDF(t) = log(1 + N/(1+df(t))). Tokens with df/N > stopListThreshold are
// penalized by stopListPenalty so ubiquitous WAF markers score ~0.
func CalculateTFIDF(_ context.Context, corpus []core.HTTPResponse) (map[string]float64, error) {
	n := float64(len(corpus))
	weights := map[string]float64{}
	if len(corpus) == 0 {
		return weights, nil
	}
	df := map[string]int{}
	for _, doc := range corpus {
		seen := map[string]bool{}
		for tok := range doc.TokenCounts {
			if !seen[tok] {
				df[tok]++
				seen[tok] = true
			}
		}
	}
	for tok, c := range df {
		idf := math.Log(1 + n/(1+float64(c)))
		if float64(c)/n > stopListThreshold {
			idf *= stopListPenalty
		}
		weights[tok] = idf
	}
	return weights, nil
}

// documentFrequencies counts in how many docs each token appears.
func documentFrequencies(corpus []core.HTTPResponse) map[string]int {
	df := map[string]int{}
	for _, doc := range corpus {
		seen := map[string]bool{}
		for tok := range doc.TokenCounts {
			if !seen[tok] {
				df[tok]++
				seen[tok] = true
			}
		}
	}
	return df
}

// DF exposes the document-frequency table (for corpus dumps / tuning).
func (e *Engine) DF() map[string]int { return e.df }

// NumDocs reports the corpus size the IDF weights were derived from.
func (e *Engine) NumDocs() int { return e.numDocs }

// Doc returns the stored sketch for an asset.
func (e *Engine) Doc(asset string) (core.HTTPResponse, bool) {
	d, ok := e.byAsset[asset]
	return d, ok
}

// InAmbiguityBand reports whether a final score falls in the LLM triage band.
func InAmbiguityBand(score float64) bool {
	return score >= AmbiguityLo && score < AmbiguityHi
}

// RarityIndex scores one document: sum over tokens of tf*idf, length
// normalized (tf = count/total). Returns 0 for empty docs.
func RarityIndex(resp core.HTTPResponse, weights map[string]float64) float64 {
	if resp.TotalTokens == 0 || len(resp.TokenCounts) == 0 {
		return 0
	}
	var sum float64
	for tok, c := range resp.TokenCounts {
		w, ok := weights[tok]
		if !ok {
			continue
		}
		sum += (float64(c) / float64(resp.TotalTokens)) * w
	}
	return sum
}

// --- Clustering (SimHash + favicon + status + title) ---

// SimHash64 computes a 64-bit SimHash over whitespace tokens. Small,
// dependency-free, good enough for near-duplicate WAF pages.
func SimHash64(text string) uint64 {
	v := make([]int, 64)
	for _, tok := range strings.Fields(strings.ToLower(text)) {
		h := fnv.New64a()
		_, _ = h.Write([]byte(tok))
		x := h.Sum64()
		for i := 0; i < 64; i++ {
			if x&(1<<uint(i)) != 0 {
				v[i]++
			} else {
				v[i]--
			}
		}
	}
	var out uint64
	for i := 0; i < 64; i++ {
		if v[i] > 0 {
			out |= 1 << uint(i)
		}
	}
	return out
}

// Hamming returns the bit distance between two SimHashes.
func Hamming(a, b uint64) int {
	x := a ^ b
	n := 0
	for x != 0 {
		n += int(x & 1)
		x >>= 1
	}
	return n
}

// ClusterKey groups near-duplicate responses. Two responses share a cluster
// only if SimHash is close AND status, favicon, and normalized title agree —
// preventing distinct apps behind one WAF from collapsing together.
func ClusterKey(resp core.HTTPResponse) string {
	title := strings.ToLower(strings.TrimSpace(resp.Title))
	h := md5.Sum([]byte(strings.Join([]string{
		resp.FaviconHash, statusBucket(resp.StatusCode), title,
	}, "|")))
	return hex.EncodeToString(h[:])[:12]
}

// SameCluster reports whether b belongs to a's cluster.
func SameCluster(a, b core.HTTPResponse) bool {
	if a.StatusCode != b.StatusCode || a.FaviconHash != b.FaviconHash {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(a.Title), strings.TrimSpace(b.Title)) {
		return false
	}
	return Hamming(a.SimHash, b.SimHash) <= 3
}

// Ambiguity band: scores in [AmbiguityLo, AmbiguityHi) are routed to the
// local LLM for semantic triage. Below the band is noise, at/above it is
// high-confidence regex territory that bypasses the LLM for speed.
const (
	AmbiguityLo = 0.15
	AmbiguityHi = 0.6
)

// --- Scorer (additive, capped, explainable) ---

// Engine is the v0.1 TriageEngine: rarity + additive capped signals.
type Engine struct {
	weights   map[string]float64
	df        map[string]int // document frequency per token (for corpus dumps)
	numDocs   int
	byAsset   map[string]core.HTTPResponse
	clSizes   map[string]int // clusterID -> size
	clusterOf map[string]string
}

var _ core.TriageEngine = (*Engine)(nil)

// NewEngine builds a scorer from a probed corpus. Token sketches are
// derived here if the caller did not pre-tokenize.
func NewEngine(ctx context.Context, corpus []core.HTTPResponse) (*Engine, error) {
	for i := range corpus {
		if corpus[i].TokenCounts == nil {
			corpus[i].TokenCounts = Tokenize(corpus[i])
			t := 0
			for _, c := range corpus[i].TokenCounts {
				t += c
			}
			corpus[i].TotalTokens = t
		}
	}
	w, err := CalculateTFIDF(ctx, corpus)
	if err != nil {
		return nil, err
	}
	e := &Engine{
		weights: w,
		df:      documentFrequencies(corpus),
		numDocs: len(corpus),
		byAsset: map[string]core.HTTPResponse{},
		clSizes: map[string]int{},
	}
	// Representative clustering: first-seen doc per composite key wins.
	repKey := map[string]string{} // clusterID -> representative asset
	for _, d := range corpus {
		e.byAsset[d.Asset] = d
		matched := ""
		for cid, repAsset := range repKey {
			if rep, ok := e.byAsset[repAsset]; ok && SameCluster(rep, d) {
				matched = cid
				break
			}
			_ = cid
		}
		if matched == "" {
			matched = ClusterKey(d)
			repKey[matched] = d.Asset
		}
		e.clSizes[matched]++
		// stash cluster id via title-suffixed map: keep simple by re-keying
		// through a second pass below.
		_ = matched
	}
	// Second pass: assign each asset its cluster id + size deterministically.
	e.clusterOf = map[string]string{}
	reps := []core.HTTPResponse{}
	for _, d := range corpus {
		placed := false
		for _, r := range reps {
			if SameCluster(r, d) {
				e.clusterOf[d.Asset] = ClusterKey(r)
				placed = true
				break
			}
		}
		if !placed {
			reps = append(reps, d)
			e.clusterOf[d.Asset] = ClusterKey(d)
		}
	}
	// Recompute sizes from final assignment.
	e.clSizes = map[string]int{}
	for _, cid := range e.clusterOf {
		e.clSizes[cid]++
	}
	return e, nil
}

// Weights exposes the IDF table (for tests/debugging).
func (e *Engine) Weights() map[string]float64 { return e.weights }

func (e *Engine) CalculateTFIDF(ctx context.Context, corpus []core.HTTPResponse) (map[string]float64, error) {
	return CalculateTFIDF(ctx, corpus)
}

// Score evaluates one asset. Additive capped model:
//
//	score = min(1, rarityNorm + loginBonus + debugBonus + apiBonus) * confidence
//
// where rarityNorm is min-max normalized RarityIndex across the corpus at
// Score time (computed lazily per call over byAsset — fine at 5k scale;
// move to precomputed norm for larger corpora).
func (e *Engine) Score(_ context.Context, asset core.Asset, _ core.EvidenceGraph) (core.TriageResult, error) {
	resp, ok := e.byAsset[asset.Name]
	if !ok {
		return core.TriageResult{
			Asset: asset.Name, Recommendation: "unprobed",
			Evidence: []string{"no probe data"},
		}, nil
	}
	rarity := RarityIndex(resp, e.weights)
	// corpus min-max for normalization
	lo, hi := math.Inf(1), math.Inf(-1)
	rarities := map[string]float64{}
	for name, d := range e.byAsset {
		r := RarityIndex(d, e.weights)
		rarities[name] = r
		if r < lo {
			lo = r
		}
		if r > hi {
			hi = r
		}
	}
	var rarityNorm float64
	if hi > lo {
		rarityNorm = (rarity - lo) / (hi - lo)
	}
	var (
		evidence []string
		bonus    float64
		conf    = 0.5
	)
	lower := strings.ToLower(asset.Name + " " + resp.Title + " " + resp.BodyPreview)
	if strings.Contains(lower, "admin") && strings.Contains(lower, "login") {
		bonus += 0.25
		evidence = append(evidence, "admin+login signals")
		conf = 0.6
	} else if strings.Contains(lower, "admin") {
		bonus += 0.1
		evidence = append(evidence, "admin keyword (weak, needs corroboration)")
		conf = 0.3
	}
	if strings.Contains(lower, "password") {
		bonus += 0.3
		evidence = append(evidence, "password input present")
		if conf < 0.95 {
			conf = 0.95
		}
	}
	for _, tok := range []string{"x-debug-token", "laravel-debug", "stack trace", "debug:true"} {
		if strings.Contains(lower, tok) {
			bonus += 0.35
			evidence = append(evidence, "debug surface: "+tok)
			if conf < 0.8 {
				conf = 0.8
			}
		}
	}
	if strings.Contains(lower, "/api/") || strings.Contains(lower, "swagger") {
		bonus += 0.15
		evidence = append(evidence, "api surface")
	}
	if len(evidence) == 0 {
		evidence = append(evidence, "no strong signals")
	}
	evidence = append(evidence, sprintfRarity(rarity))
	score := rarityNorm + bonus
	if score > 1 {
		score = 1
	}
	final := score * conf
	cid := e.clusterOf[asset.Name]
	rec := "ignore"
	switch {
	case final >= 0.6:
		rec = "manual_review_high_priority"
	case final >= 0.3:
		rec = "manual_review_medium"
	case final >= 0.1:
		rec = "bulk_review_low"
	}
	return core.TriageResult{
		Asset: asset.Name, FinalScore: round3(final), Confidence: conf,
		RarityIndex: round3(rarity), Evidence: evidence,
		ClusterID: cid, ClusterSize: e.clSizes[cid],
		Final: true, Recommendation: rec,
	}, nil
}

// TopN returns the top-N assets by final score (for nuclei gating).
func (e *Engine) TopN(ctx context.Context, g core.EvidenceGraph, n int) ([]core.TriageResult, error) {
	var out []core.TriageResult
	for name := range e.byAsset {
		r, err := e.Score(ctx, core.Asset{Name: name}, g)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FinalScore > out[j].FinalScore })
	if len(out) > n {
		out = out[:n]
	}
	return out, nil
}

func sprintfRarity(r float64) string {
	return "rarity=" + format3(r)
}

func format3(f float64) string {
	return strings.TrimRight(strings.TrimRight(sprintf3(f), "0"), ".")
}

func sprintf3(f float64) string {
	// avoid fmt import cycle weight; use simple formatting via math
	neg := f < 0
	if neg {
		f = -f
	}
	i := int(f*1000 + 0.5)
	s := itoa(i/1000) + "." + pad3(i%1000)
	if neg {
		return "-" + s
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [32]byte
	p := len(b)
	for n > 0 {
		p--
		b[p] = byte('0' + n%10)
		n /= 10
	}
	return string(b[p:])
}

func pad3(n int) string {
	s := itoa(n)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }
