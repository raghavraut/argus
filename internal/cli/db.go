package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/raghavraut/rarefy/internal/state"
)

func newDB() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Inspect and manage the SQLite resume store",
	}
	cmd.AddCommand(newDBStats(), newDBReset())
	return cmd
}

func newDBStats() *cobra.Command {
	var dbPath string
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show per-campaign task/graph totals",
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := state.Open(dbPath)
			if err != nil {
				return fmt.Errorf("state: %w", err)
			}
			defer func() { _ = store.Close() }()
			stats, err := store.ListCampaigns(cmd.Context())
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(stats)
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "rarefy.db", "SQLite state path")
	return cmd
}

func newDBReset() *cobra.Command {
	var dbPath, campaign string
	var all bool
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Delete resume state (one campaign or everything)",
		Example: `  rarefy db reset --campaign target.com
  rarefy db reset --all`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !all && campaign == "" {
				return fmt.Errorf("pass --campaign NAME or --all")
			}
			target := campaign
			if all {
				target = ""
			}
			store, err := state.Open(dbPath)
			if err != nil {
				return fmt.Errorf("state: %w", err)
			}
			defer func() { _ = store.Close() }()
			n, err := store.ResetCampaign(cmd.Context(), target)
			if err != nil {
				return err
			}
			cmd.Printf("reset %d rows", n)
			if all {
				cmd.Printf(" (all campaigns)")
			} else {
				cmd.Printf(" (campaign %q)", target)
			}
			cmd.Printf("\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "rarefy.db", "SQLite state path")
	cmd.Flags().StringVar(&campaign, "campaign", "", "campaign to wipe")
	cmd.Flags().BoolVar(&all, "all", false, "wipe every campaign")
	return cmd
}
