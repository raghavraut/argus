package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/raghavraut/rarefy/internal/output"
	"github.com/raghavraut/rarefy/internal/state"
)

type filterOpts struct {
	dbPath        string
	campaign      string
	minScore      float64
	minConfidence float64
	tech          []string
	format        string
	limit         int
}

func newFilter() *cobra.Command {
	o := &filterOpts{}
	cmd := &cobra.Command{
		Use:   "filter",
		Short: "Slice persisted verdicts by score, confidence and tech",
		Example: `  rarefy filter --min-score 0.6 --format urls > top_targets.txt
  rarefy filter --tech Jenkins --format jsonl
  rarefy filter --campaign target.com --format markdown`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runFilter(cmd, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.dbPath, "db", "rarefy.db", "SQLite state path")
	f.StringVar(&o.campaign, "campaign", "", "campaign id (defaults to most recent)")
	f.Float64Var(&o.minScore, "min-score", 0, "minimum final score (0-1)")
	f.Float64Var(&o.minConfidence, "min-confidence", 0, "minimum confidence (0-1)")
	f.StringSliceVar(&o.tech, "tech", nil, "tech substring filter, case-insensitive (repeatable/comma-separated)")
	f.StringVar(&o.format, "format", "urls", "output format: urls|jsonl|markdown")
	f.IntVar(&o.limit, "limit", 0, "max rows (0 = unlimited)")
	return cmd
}

func runFilter(cmd *cobra.Command, o *filterOpts) error {
	log := output.Logger("[rarefy] ")
	switch strings.ToLower(o.format) {
	case "urls", "jsonl", "markdown":
	default:
		return fmt.Errorf("unknown format %q (want urls|jsonl|markdown)", o.format)
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
		log.Printf("filter: no --campaign given, using most recent %q", camp)
	}
	rows, err := store.QueryResults(ctx, camp, state.FilterParams{
		MinScore: o.minScore, MinConfidence: o.minConfidence,
		Tech: o.tech, Limit: o.limit,
	})
	if err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	switch strings.ToLower(o.format) {
	case "urls":
		for _, r := range rows {
			fmt.Fprintf(w, "https://%s\n", r.Result.Asset)
		}
	case "jsonl":
		enc := json.NewEncoder(w)
		for _, r := range rows {
			if err := enc.Encode(r.Result); err != nil {
				return err
			}
		}
	case "markdown":
		fmt.Fprintf(w, "# Rarefy triage — %s (%d rows)\n\n", camp, len(rows))
		fmt.Fprintf(w, "| Asset | Score | Conf | Rarity | Status | Tech | Recommendation |\n")
		fmt.Fprintf(w, "|---|---|---|---|---|---|---|\n")
		for _, r := range rows {
			fmt.Fprintf(w, "| %s | %.3f | %.2f | %.3f | %d | %s | %s |\n",
				mdEscape(r.Result.Asset), r.Result.FinalScore, r.Result.Confidence,
				r.Result.RarityIndex, r.Status, mdEscape(strings.Join(r.Tech, ", ")),
				mdEscape(r.Result.Recommendation))
		}
	}
	log.Printf("filter: campaign=%q rows=%d format=%s", camp, len(rows), o.format)
	return nil
}

func mdEscape(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
