package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/raghavraut/rarefy/internal/output"
	"github.com/raghavraut/rarefy/internal/state"
)

type evalOpts struct {
	dbPath   string
	campaign string
	labels   string
	minScore float64
}

func newEval() *cobra.Command {
	o := &evalOpts{}
	cmd := &cobra.Command{
		Use:   "eval --labels labels.jsonl",
		Short: "Score triage precision/recall against labeled ground truth",
		Long: `Compares persisted verdicts against a labeled corpus and reports
precision/recall/F1 for both the triage scorer (score >= --min-score)
and a naive baseline (any nuclei finding = interesting).

Labels file: one JSON object per line: {"asset": "a.t", "is_interesting": true}.
Assets in labels but missing from the campaign are reported as unlabeled
coverage loss, not counted in either direction.`,
		Example: `  rarefy eval --campaign target.com --labels labels.jsonl
  rarefy eval --labels labels.jsonl --min-score 0.6`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runEval(cmd, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.dbPath, "db", "rarefy.db", "SQLite state path")
	f.StringVar(&o.campaign, "campaign", "", "campaign id (defaults to most recent)")
	f.StringVar(&o.labels, "labels", "", "labeled corpus JSONL (required)")
	f.Float64Var(&o.minScore, "min-score", 0.3, "triage positive threshold")
	_ = cmd.MarkFlagRequired("labels")
	return cmd
}

// metrics is a confusion matrix with derived scores.
type metrics struct {
	tp, fp, tn, fn int
}

func (m metrics) precision() float64 {
	if m.tp+m.fp == 0 {
		return 0
	}
	return float64(m.tp) / float64(m.tp+m.fp)
}

func (m metrics) recall() float64 {
	if m.tp+m.fn == 0 {
		return 0
	}
	return float64(m.tp) / float64(m.tp+m.fn)
}

func (m metrics) f1() float64 {
	p, r := m.precision(), m.recall()
	if p+r == 0 {
		return 0
	}
	return 2 * p * r / (p + r)
}

// computeMetrics tallies predictions against ground truth over their
// intersection. Assets predicted but unlabeled, or labeled but unscored,
// are the caller's coverage problem — never silently counted.
func computeMetrics(truth map[string]bool, predicted map[string]bool) metrics {
	var m metrics
	for asset, want := range truth {
		got, scored := predicted[asset]
		if !scored {
			continue
		}
		switch {
		case got && want:
			m.tp++
		case got && !want:
			m.fp++
		case !got && !want:
			m.tn++
		default:
			m.fn++
		}
	}
	return m
}

func loadLabels(path string) (map[string]bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	out := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		var rec struct {
			Asset         string `json:"asset"`
			IsInteresting bool   `json:"is_interesting"`
		}
		if err := json.Unmarshal([]byte(text), &rec); err != nil {
			return nil, fmt.Errorf("labels line %d: %w", line, err)
		}
		if rec.Asset == "" {
			return nil, fmt.Errorf("labels line %d: empty asset", line)
		}
		out[strings.ToLower(rec.Asset)] = rec.IsInteresting
	}
	return out, sc.Err()
}

func runEval(cmd *cobra.Command, o *evalOpts) error {
	log := output.Logger("[rarefy] ")
	truth, err := loadLabels(o.labels)
	if err != nil {
		return err
	}
	if len(truth) == 0 {
		return fmt.Errorf("no labels in %s", o.labels)
	}
	store, err := state.Open(o.dbPath)
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	defer func() { _ = store.Close() }()

	ctx := cmd.Context()
	camp := o.campaign
	if camp == "" {
		camp, err = store.LatestCampaign(ctx)
		if err != nil {
			return fmt.Errorf("latest campaign: %w", err)
		}
		if camp == "" {
			return fmt.Errorf("no campaigns in %s: run `rarefy scan` first", o.dbPath)
		}
	}
	rows, err := store.QueryResults(ctx, camp, state.FilterParams{})
	if err != nil {
		return err
	}
	counts, err := store.FindingCounts(ctx, camp)
	if err != nil {
		return err
	}
	triagePred := make(map[string]bool, len(rows))
	for _, r := range rows {
		triagePred[strings.ToLower(r.Result.Asset)] = r.Result.FinalScore >= o.minScore
	}
	naivePred := make(map[string]bool, len(rows))
	for _, r := range rows {
		naivePred[strings.ToLower(r.Result.Asset)] = counts[r.Result.Asset] > 0
	}
	covered, missing := 0, []string{}
	for asset := range truth {
		scored := false
		for _, r := range rows {
			if strings.ToLower(r.Result.Asset) == asset {
				scored = true
				break
			}
		}
		if scored {
			covered++
		} else if len(missing) < 10 {
			missing = append(missing, asset)
		}
	}
	tm, nm := computeMetrics(truth, triagePred), computeMetrics(truth, naivePred)

	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "eval campaign=%s labels=%d covered=%d min-score=%.2f\n\n", camp, len(truth), covered, o.minScore)
	fmt.Fprintf(w, "%-10s %4s %4s %4s %4s %6s %6s %6s\n", "model", "tp", "fp", "tn", "fn", "prec", "rec", "f1")
	for _, row := range []struct {
		name string
		m    metrics
	}{{"triage", tm}, {"naive", nm}} {
		fmt.Fprintf(w, "%-10s %4d %4d %4d %4d %6.3f %6.3f %6.3f\n",
			row.name, row.m.tp, row.m.fp, row.m.tn, row.m.fn,
			row.m.precision(), row.m.recall(), row.m.f1())
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		fmt.Fprintf(w, "\nlabeled but unscored (excluded): %s\n", strings.Join(missing, ", "))
	}
	log.Printf("eval: campaign=%q triage_f1=%.3f naive_f1=%.3f", camp, tm.f1(), nm.f1())
	return nil
}
