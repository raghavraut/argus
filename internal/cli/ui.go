package cli

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/raghavraut/rarefy/internal/output"
	"github.com/raghavraut/rarefy/internal/state"
	"github.com/raghavraut/rarefy/internal/ui"
)

type uiOpts struct {
	dbPath   string
	campaign string
	port     int
	open     bool
}

func newUI() *cobra.Command {
	o := &uiOpts{}
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Serve the local triage dashboard (graph + corpus views)",
		Example: `  rarefy ui --port 8080
  rarefy ui --campaign target.com --open`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUI(cmd.Context(), o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.dbPath, "db", "rarefy.db", "SQLite state path")
	f.StringVar(&o.campaign, "campaign", "", "campaign to serve (defaults to most recent)")
	f.IntVar(&o.port, "port", 8080, "local port to listen on")
	f.BoolVar(&o.open, "open", false, "open the dashboard in the default browser")
	return cmd
}

func runUI(ctx context.Context, o *uiOpts) error {
	log := output.Logger("[rarefy] ")
	store, err := state.Open(o.dbPath)
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	defer func() { _ = store.Close() }()

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
	srv, err := ui.New(store, camp)
	if err != nil {
		return err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", o.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	url := fmt.Sprintf("http://%s", ln.Addr().String())
	log.Printf("ui: campaign=%q serving %s (Ctrl+C to stop)", camp, url)
	if o.open {
		openBrowser(url)
	}

	httpSrv := &http.Server{Handler: srv, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()
	if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// openBrowser is best-effort; failures only log.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		output.Logger("[rarefy] ").Printf("ui: could not open browser: %v", err)
	}
}
