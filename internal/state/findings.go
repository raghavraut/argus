// Package state — findings.go: nuclei post-processing persistence.
package state

import (
	"context"
)

// StoredFinding is one nuclei verdict row.
type StoredFinding struct {
	Asset      string `json:"asset"`
	TemplateID string `json:"template_id"`
	Severity   string `json:"severity"`
	Name       string `json:"name"`
	Matched    string `json:"matched"`
	URL        string `json:"url"`
}

// SaveFindings upserts findings in one transaction (reruns never duplicate).
func (s *Store) SaveFindings(ctx context.Context, campaign string, rows []StoredFinding) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO
findings(campaign,asset,template_id,severity,name,matched,url)
VALUES(?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, campaign, r.Asset, r.TemplateID,
			r.Severity, r.Name, r.Matched, r.URL); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// LoadFindings returns findings for a campaign (all assets) or one asset.
func (s *Store) LoadFindings(ctx context.Context, campaign, asset string) ([]StoredFinding, error) {
	q := `SELECT asset,template_id,severity,name,matched,url FROM findings WHERE campaign=?`
	args := []any{campaign}
	if asset != "" {
		q += ` AND asset=?`
		args = append(args, asset)
	}
	q += ` ORDER BY asset,severity DESC,template_id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []StoredFinding
	for rows.Next() {
		var f StoredFinding
		if err := rows.Scan(&f.Asset, &f.TemplateID, &f.Severity, &f.Name, &f.Matched, &f.URL); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FindingCounts returns per-asset finding totals for a campaign.
func (s *Store) FindingCounts(ctx context.Context, campaign string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT asset,COUNT(*) FROM findings WHERE campaign=? GROUP BY asset`, campaign)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var asset string
		var n int
		if err := rows.Scan(&asset, &n); err != nil {
			return nil, err
		}
		out[asset] = n
	}
	return out, rows.Err()
}
