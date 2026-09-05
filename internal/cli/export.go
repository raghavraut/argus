package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/raghavraut/argus/internal/export"
	"github.com/raghavraut/argus/internal/output"
	"github.com/raghavraut/argus/internal/state"
)

type exportOpts struct {
	dbPath   string
	campaign string
	format   string
	out      string
	maxNodes int
}

func newExport() *cobra.Command {
	o := &exportOpts{}
	cmd := &cobra.Command{
		Use:   "export [--format dot|mermaid]",
		Short: "Render the persisted evidence graph (Graphviz DOT or Mermaid)",
		Example: `  argus export --campaign target.com --format dot --out surface.dot
  argus export --format mermaid | clip`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runExport(cmd, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.dbPath, "db", "argus.db", "SQLite state path")
	f.StringVar(&o.campaign, "campaign", "", "campaign to export (defaults to most active)")
	f.StringVar(&o.format, "format", "dot", "output format: dot|mermaid")
	f.StringVarP(&o.out, "out", "o", "", "output file (defaults to stdout)")
	f.IntVar(&o.maxNodes, "max-nodes", 300, "cap rendered nodes, assets-first (0 = unlimited)")
	return cmd
}

func runExport(cmd *cobra.Command, o *exportOpts) error {
	log := output.Logger("[argus] ")
	store, err := state.Open(o.dbPath)
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	defer func() { _ = store.Close() }()

	ctx := cmd.Context()
	camp := o.campaign
	if camp == "" {
		stats, err := store.ListCampaigns(ctx)
		if err != nil {
			return fmt.Errorf("list campaigns: %w", err)
		}
		if len(stats) == 0 {
			return fmt.Errorf("no campaigns in %s: run `argus scan` first", o.dbPath)
		}
		camp = stats[0].Campaign
		log.Printf("export: no --campaign given, using most active %q", camp)
	}
	nodes, edges, err := store.LoadGraph(ctx, camp)
	if err != nil {
		return fmt.Errorf("load graph: %w", err)
	}
	if len(nodes) == 0 {
		return fmt.Errorf("campaign %q has no persisted graph: re-run `argus scan` to persist it", camp)
	}

	w := cmd.OutOrStdout()
	if o.out != "" {
		f, err := os.Create(o.out)
		if err != nil {
			return fmt.Errorf("create %s: %w", o.out, err)
		}
		defer func() { _ = f.Close() }()
		w = f
	}
	if err := export.Write(w, o.format, nodes, edges, export.Options{MaxNodes: o.maxNodes}); err != nil {
		return err
	}
	log.Printf("export: campaign=%q format=%s nodes=%d edges=%d", camp, o.format, len(nodes), len(edges))
	return nil
}
