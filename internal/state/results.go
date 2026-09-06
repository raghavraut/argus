// Package state — results.go: TriageResult persistence + the filter engine.
//
// results stores one row per (campaign, asset): the exact JSONL blob that
// was emitted on stdout plus indexed columns (score, confidence, tech)
// so `rarefy filter` and `rarefy ui` can slice without re-scoring.
// All WHERE values are bound parameters — filter flags never touch SQL text.
package state

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/raghavraut/rarefy/internal/core"
)

// ResultRow is one persisted final verdict with its probe context.
type ResultRow struct {
	Result  core.TriageResult
	Status  int               `json:"status"`
	Title   string            `json:"title"`
	Tech    []string          `json:"tech"`
	Headers map[string]string `json:"headers"`
}

// SaveResults upserts final verdicts (plus headers/tech context) in one tx.
func (s *Store) SaveResults(ctx context.Context, campaign string, rows []ResultRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO
results(campaign,asset,score,confidence,rarity,status,title,tech_json,headers_json,result_json)
VALUES(?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, r := range rows {
		tech, _ := json.Marshal(nonNilStrings(r.Tech))
		headers, _ := json.Marshal(nonNilMap(r.Headers))
		blob, _ := json.Marshal(r.Result)
		if _, err := stmt.ExecContext(ctx, campaign, r.Result.Asset,
			r.Result.FinalScore, r.Result.Confidence, r.Result.RarityIndex,
			r.Status, r.Title, string(tech), string(headers), string(blob)); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// FilterParams translates CLI flags to a parameterized query.
// Zero values mean "no constraint".
type FilterParams struct {
	MinScore      float64
	MinConfidence float64
	Tech          []string // OR-matched, case-insensitive substring
	Limit         int      // 0 = unlimited
}

// QueryResults returns verdicts newest-score-first honoring every filter.
func (s *Store) QueryResults(ctx context.Context, campaign string, f FilterParams) ([]ResultRow, error) {
	var (
		sb   strings.Builder
		args []any
	)
	sb.WriteString(`SELECT score,confidence,rarity,status,title,tech_json,headers_json,result_json
FROM results WHERE campaign=?`)
	args = append(args, campaign)
	if f.MinScore > 0 {
		sb.WriteString(` AND score>=?`)
		args = append(args, f.MinScore)
	}
	if f.MinConfidence > 0 {
		sb.WriteString(` AND confidence>=?`)
		args = append(args, f.MinConfidence)
	}
	var ors []string
	for _, t := range f.Tech {
		if strings.TrimSpace(t) == "" {
			continue
		}
		// Bound parameter: the LIKE pattern is data, never SQL text.
		ors = append(ors, `lower(tech_json) LIKE '%'||lower(?)||'%'`)
		args = append(args, t)
	}
	if len(ors) > 0 {
		sb.WriteString(` AND (` + strings.Join(ors, " OR ") + `)`)
	}
	sb.WriteString(` ORDER BY score DESC, asset ASC`)
	if f.Limit > 0 {
		sb.WriteString(` LIMIT ?`)
		args = append(args, f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ResultRow
	for rows.Next() {
		var (
			r          ResultRow
			techJ, headJ, blob string
		)
		if err := rows.Scan(&r.Result.FinalScore, &r.Result.Confidence,
			&r.Result.RarityIndex, &r.Status, &r.Title, &techJ, &headJ, &blob); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(techJ), &r.Tech)
		r.Headers = map[string]string{}
		_ = json.Unmarshal([]byte(headJ), &r.Headers)
		if err := json.Unmarshal([]byte(blob), &r.Result); err != nil {
			return nil, fmt.Errorf("corrupt result blob: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ResultCount counts persisted verdicts for a campaign.
func (s *Store) ResultCount(ctx context.Context, campaign string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM results WHERE campaign=?`, campaign).Scan(&n)
	return n, err
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func nonNilMap(in map[string]string) map[string]string {
	if in == nil {
		return map[string]string{}
	}
	return in
}
