// Package state provides SQLite-backed resumability.
//
// Write-amplification fix: task completions are buffered in memory and
// flushed in a single transaction every flushInterval or flushSize
// completions (plus on Close), instead of one fsync per task. WAL mode +
// busy_timeout keep readers from blocking the single writer.
package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"time"

	"github.com/argus/argus/internal/core"

	_ "modernc.org/sqlite"
)

const (
	flushInterval = 500 * time.Millisecond
	flushSize     = 100
)

// Store tracks completed (campaign, asset, stage) units.
type Store struct {
	db     *sql.DB
	mu     sync.Mutex
	pending map[string]struct{} // key: campaign+"\x00"+asset+"\x00"+stage

	flushCh chan struct{}
	done    chan struct{}
	wg      sync.WaitGroup
}

// key builds the dedupe key.
func key(campaign, asset, stage string) string {
	return campaign + "\x00" + asset + "\x00" + stage
}

// Open creates/opens the DB, applies pragmas, and migrates schema.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	for _, pragma := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA busy_timeout=5000`,
		`PRAGMA foreign_keys=ON`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	schema := `
CREATE TABLE IF NOT EXISTS tasks(
  campaign TEXT NOT NULL,
  asset TEXT NOT NULL,
  stage TEXT NOT NULL,
  done_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  PRIMARY KEY(campaign, asset, stage)
);
CREATE TABLE IF NOT EXISTS assets(
  campaign TEXT NOT NULL,
  asset TEXT NOT NULL,
  status INTEGER NOT NULL DEFAULT 0,
  title TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(campaign, asset)
);
CREATE TABLE IF NOT EXISTS graph_nodes(
  campaign TEXT NOT NULL,
  id TEXT NOT NULL,
  type TEXT NOT NULL DEFAULT '',
  score REAL NOT NULL DEFAULT 0,
  attrs TEXT NOT NULL DEFAULT '{}',
  PRIMARY KEY(campaign, id)
);
CREATE TABLE IF NOT EXISTS graph_edges(
  campaign TEXT NOT NULL,
  from_id TEXT NOT NULL,
  to_id TEXT NOT NULL,
  type TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(campaign, from_id, to_id, type)
);
CREATE TABLE IF NOT EXISTS results(
  campaign TEXT NOT NULL,
  asset TEXT NOT NULL,
  score REAL NOT NULL DEFAULT 0,
  confidence REAL NOT NULL DEFAULT 0,
  rarity REAL NOT NULL DEFAULT 0,
  status INTEGER NOT NULL DEFAULT 0,
  title TEXT NOT NULL DEFAULT '',
  tech_json TEXT NOT NULL DEFAULT '[]',
  headers_json TEXT NOT NULL DEFAULT '{}',
  result_json TEXT NOT NULL DEFAULT '{}',
  PRIMARY KEY(campaign, asset)
);
CREATE TABLE IF NOT EXISTS findings(
  campaign TEXT NOT NULL,
  asset TEXT NOT NULL,
  template_id TEXT NOT NULL,
  severity TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  matched TEXT NOT NULL DEFAULT '',
  url TEXT NOT NULL DEFAULT '',
  found_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  PRIMARY KEY(campaign, asset, template_id, matched)
);`
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{
		db: db, pending: map[string]struct{}{},
		flushCh: make(chan struct{}, 1), done: make(chan struct{}),
	}
	s.wg.Add(1)
	go s.flushLoop()
	return s, nil
}

func (s *Store) flushLoop() {
	defer s.wg.Done()
	t := time.NewTicker(flushInterval)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			_ = s.flush()
			return
		case <-t.C:
			_ = s.flush()
		case <-s.flushCh:
			_ = s.flush()
		}
	}
}

// MarkDone buffers a completion; it is durable after the next flush/Close.
func (s *Store) MarkDone(campaign, asset, stage string) {
	s.mu.Lock()
	s.pending[key(campaign, asset, stage)] = struct{}{}
	n := len(s.pending)
	s.mu.Unlock()
	if n >= flushSize {
		select {
		case s.flushCh <- struct{}{}:
		default:
		}
	}
}

// IsDone reports whether a unit completed in a prior run.
func (s *Store) IsDone(ctx context.Context, campaign, asset, stage string) (bool, error) {
	s.mu.Lock()
	_, pend := s.pending[key(campaign, asset, stage)]
	s.mu.Unlock()
	if pend {
		return true, nil
	}
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM tasks WHERE campaign=? AND asset=? AND stage=?`,
		campaign, asset, stage).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// Flush forces pending completions to disk in one transaction.
func (s *Store) Flush() error { return s.flush() }

func (s *Store) flush() error {
	s.mu.Lock()
	if len(s.pending) == 0 {
		s.mu.Unlock()
		return nil
	}
	batch := make([]string, 0, len(s.pending))
	for k := range s.pending {
		batch = append(batch, k)
	}
	clear(s.pending)
	s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO tasks(campaign,asset,stage) VALUES(?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, k := range batch {
		// split key without strings package overhead concerns — simple scan
		c, a, st := splitKey(k)
		if _, err := stmt.Exec(c, a, st); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return err
		}
	}
	_ = stmt.Close()
	return tx.Commit()
}

func splitKey(k string) (c, a, s string) {
	i := indexByte(k, 0)
	j := indexByte(k, i+1)
	return k[:i], k[i+1 : j], k[j+1:]
}

func indexByte(s string, from int) int {
	for i := from; i < len(s); i++ {
		if s[i] == 0 {
			return i
		}
	}
	return len(s)
}

// Close flushes and closes the DB.
func (s *Store) Close() error {
	close(s.done)
	s.wg.Wait()
	return s.db.Close()
}

// --- Graph persistence (powers `argus export`) ---

// SaveGraph replaces the campaign's stored graph snapshot in one transaction.
// Idempotent: re-running a campaign overwrites, never duplicates.
func (s *Store) SaveGraph(ctx context.Context, campaign string, nodes []core.Node, edges []core.Edge) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM graph_edges WHERE campaign=?`, campaign); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM graph_nodes WHERE campaign=?`, campaign); err != nil {
		_ = tx.Rollback()
		return err
	}
	ns, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO graph_nodes(campaign,id,type,score,attrs) VALUES(?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, n := range nodes {
		attrs, _ := json.Marshal(n.Attrs)
		if _, err := ns.ExecContext(ctx, campaign, n.ID, string(n.Type), n.Score, string(attrs)); err != nil {
			_ = ns.Close()
			_ = tx.Rollback()
			return err
		}
	}
	_ = ns.Close()
	es, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO graph_edges(campaign,from_id,to_id,type) VALUES(?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, e := range edges {
		if _, err := es.ExecContext(ctx, campaign, e.From, e.To, string(e.Type)); err != nil {
			_ = es.Close()
			_ = tx.Rollback()
			return err
		}
	}
	_ = es.Close()
	return tx.Commit()
}

// LoadGraph reads back a campaign snapshot for exporters.
func (s *Store) LoadGraph(ctx context.Context, campaign string) ([]core.Node, []core.Edge, error) {
	nrows, err := s.db.QueryContext(ctx,
		`SELECT id,type,score,attrs FROM graph_nodes WHERE campaign=? ORDER BY id`, campaign)
	if err != nil {
		return nil, nil, err
	}
	var nodes []core.Node
	for nrows.Next() {
		var n core.Node
		var typ, attrs string
		if err := nrows.Scan(&n.ID, &typ, &n.Score, &attrs); err != nil {
			_ = nrows.Close()
			return nil, nil, err
		}
		n.Type = core.NodeType(typ)
		n.Attrs = map[string]string{}
		_ = json.Unmarshal([]byte(attrs), &n.Attrs)
		nodes = append(nodes, n)
	}
	_ = nrows.Close()
	if err := nrows.Err(); err != nil {
		return nil, nil, err
	}
	erows, err := s.db.QueryContext(ctx,
		`SELECT from_id,to_id,type FROM graph_edges WHERE campaign=? ORDER BY from_id,to_id,type`, campaign)
	if err != nil {
		return nil, nil, err
	}
	var edges []core.Edge
	for erows.Next() {
		var e core.Edge
		var typ string
		if err := erows.Scan(&e.From, &e.To, &typ); err != nil {
			_ = erows.Close()
			return nil, nil, err
		}
		e.Type = core.EdgeType(typ)
		edges = append(edges, e)
	}
	_ = erows.Close()
	return nodes, edges, erows.Err()
}

// CampaignStats summarizes one campaign for `argus db stats`.
type CampaignStats struct {
	Campaign string `json:"campaign"`
	Tasks    int    `json:"tasks"`
	Nodes    int    `json:"nodes"`
	Edges    int    `json:"edges"`
	Results  int    `json:"results"`
}

// ListCampaigns returns per-campaign totals, most-active first.
func (s *Store) ListCampaigns(ctx context.Context) ([]CampaignStats, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT c.campaign,
  COALESCE((SELECT COUNT(*) FROM tasks t WHERE t.campaign=c.campaign),0),
  COALESCE((SELECT COUNT(*) FROM graph_nodes n WHERE n.campaign=c.campaign),0),
  COALESCE((SELECT COUNT(*) FROM graph_edges e WHERE e.campaign=c.campaign),0),
  COALESCE((SELECT COUNT(*) FROM results r WHERE r.campaign=c.campaign),0)
FROM (SELECT campaign FROM tasks UNION SELECT campaign FROM graph_nodes
      UNION SELECT campaign FROM graph_edges UNION SELECT campaign FROM results) c
ORDER BY 2 DESC, c.campaign`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []CampaignStats
	for rows.Next() {
		var st CampaignStats
		if err := rows.Scan(&st.Campaign, &st.Tasks, &st.Nodes, &st.Edges, &st.Results); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// LatestCampaign returns the most recently active campaign (by task clock).
func (s *Store) LatestCampaign(ctx context.Context) (string, error) {
	var camp sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT campaign FROM tasks ORDER BY done_at DESC LIMIT 1`).Scan(&camp)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return camp.String, nil
}

// ResetCampaign deletes all state (tasks + graph) for one campaign.
// Pass "" to wipe every campaign.
func (s *Store) ResetCampaign(ctx context.Context, campaign string) (int64, error) {
	var total int64
	if campaign == "" {
		for _, tbl := range []string{"tasks", "graph_nodes", "graph_edges", "assets", "results", "findings"} {
			res, err := s.db.ExecContext(ctx, `DELETE FROM `+tbl)
			if err != nil {
				return total, err
			}
			n, _ := res.RowsAffected()
			total += n
		}
		return total, nil
	}
	for _, q := range []struct {
		query string
		args  []any
	}{
		{`DELETE FROM tasks WHERE campaign=?`, []any{campaign}},
		{`DELETE FROM graph_nodes WHERE campaign=?`, []any{campaign}},
		{`DELETE FROM graph_edges WHERE campaign=?`, []any{campaign}},
		{`DELETE FROM results WHERE campaign=?`, []any{campaign}},
		{`DELETE FROM findings WHERE campaign=?`, []any{campaign}},
		{`DELETE FROM assets WHERE campaign=?`, []any{campaign}},
	} {
		res, err := s.db.ExecContext(ctx, q.query, q.args...)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}
