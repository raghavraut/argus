package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/raghavraut/rarefy/internal/core"
	"github.com/raghavraut/rarefy/internal/dag"
	"github.com/raghavraut/rarefy/internal/graph"
	"github.com/raghavraut/rarefy/internal/llm"
	"github.com/raghavraut/rarefy/internal/nuclei"
	"github.com/raghavraut/rarefy/internal/output"
	"github.com/raghavraut/rarefy/internal/probe"
	"github.com/raghavraut/rarefy/internal/state"
	"github.com/raghavraut/rarefy/internal/triage"
)

// scanOpts binds every `rarefy scan` flag.
type scanOpts struct {
	domain       string
	listFile     string
	profile      string
	dbPath       string
	campaign     string
	ollamaURL    string
	ollamaModel  string
	workers      int
	threads      int
	exportCorpus string
	// Nuclei Top-N post-processing (bounty-safe tag allowlist).
	nucleiEnable   bool
	nucleiTags     []string
	nucleiExTags   []string
	nucleiMinScore float64
	nucleiMaxHosts int
	nucleiTimeout  time.Duration
	nucleiTplDir   string
}

func newScan() *cobra.Command {
	o := &scanOpts{}
	cmd := &cobra.Command{
		Use:   "scan [-d domain] [-l list] [target...]",
		Short: "Probe targets, score with TF-IDF rarity, stream JSONL to stdout",
		Example: `  rarefy scan -d target.com --profile stealth | nuclei -tags exposure
  rarefy scan -l subs.txt --profile standard
  echo evil.target.com | rarefy scan --export-corpus corpus.json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScan(cmd.Context(), args, o)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&o.domain, "domain", "d", "", "target domain (comma-separated; use -l for lists)")
	f.StringVarP(&o.listFile, "list", "l", "", "BYOS: file with one subdomain/URL per line, injected straight into the DAG (skips enumeration)")
	f.StringVar(&o.profile, "profile", "standard", "execution profile: stealth|standard|aggressive")
	f.StringVar(&o.dbPath, "db", "rarefy.db", "SQLite state path for resume")
	f.StringVar(&o.campaign, "campaign", "", "campaign id (defaults to -d value or timestamp)")
	f.StringVar(&o.ollamaURL, "ollama", "http://localhost:11434", "Ollama base URL (empty disables LLM)")
	f.StringVar(&o.ollamaModel, "model", "llama3.1:8b", "Ollama model for ambiguous triage")
	f.IntVar(&o.workers, "workers", 8, "DAG scoring workers")
	f.IntVar(&o.threads, "threads", 25, "httpx threads (single internal pool)")
	f.StringVar(&o.exportCorpus, "export-corpus", "", "write TF-IDF sketches, df table and band scores to FILE (live-fire tuning)")
	f.BoolVar(&o.nucleiEnable, "nuclei", true, "run nuclei exposure/misconfig templates over Top-N hosts post-scoring")
	f.StringSliceVar(&o.nucleiTags, "nuclei-tags", append([]string{}, nuclei.DefaultTags...), "nuclei tag allowlist (comma-separated)")
	f.StringSliceVar(&o.nucleiExTags, "nuclei-exclude-tags", append([]string{}, nuclei.DefaultExcludeTags...), "nuclei tag denylist, applied after allowlist (empty to disable)")
	f.Float64Var(&o.nucleiMinScore, "nuclei-min-score", nuclei.DefaultMinScore, "nuclei gate: minimum triage score")
	f.IntVar(&o.nucleiMaxHosts, "nuclei-max-hosts", nuclei.DefaultMaxHosts, "nuclei gate: max hosts scanned")
	f.DurationVar(&o.nucleiTimeout, "nuclei-timeout", nuclei.DefaultTimeout, "nuclei hard timeout (hanging templates cannot stall the pipeline)")
	f.StringVar(&o.nucleiTplDir, "nuclei-templates", "", "custom nuclei-templates dir (default: auto-detect; missing bundle skips nuclei, never downloads)")
	return cmd
}

func runScan(ctx context.Context, args []string, o *scanOpts) error {
	log := output.Logger("[rarefy] ")
	out := output.NewWriter(os.Stdout)

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	profile := core.ExecutionProfile(strings.ToLower(o.profile))
	switch profile {
	case core.ProfileStealth, core.ProfileStandard, core.ProfileAggressive:
	default:
		log.Printf("unknown profile %q, using standard", o.profile)
		profile = core.ProfileStandard
	}

	targets, err := loadTargets(o.domain, o.listFile, args)
	if err != nil {
		return fmt.Errorf("input: %w", err)
	}
	if len(targets) == 0 {
		return fmt.Errorf("no targets: pass -d, -l, args, or stdin")
	}
	log.Printf("targets=%d profile=%s", len(targets), profile)

	camp := o.campaign
	if camp == "" {
		if o.domain != "" {
			camp = strings.Split(o.domain, ",")[0]
		} else {
			camp = fmt.Sprintf("campaign-%d", time.Now().Unix())
		}
	}

	store, err := state.Open(o.dbPath)
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			log.Printf("state close: %v", err)
		}
	}()

	// Resume filter: skip assets already probed in this campaign.
	var pending []string
	for _, t := range targets {
		done, err := store.IsDone(ctx, camp, t, "probe")
		if err != nil {
			return fmt.Errorf("state check: %w", err)
		}
		if done {
			log.Printf("resume: skip %s (already probed)", t)
			continue
		}
		pending = append(pending, t)
	}
	if len(pending) == 0 {
		log.Printf("nothing to probe; all %d targets already done in campaign %q", len(targets), camp)
		return nil
	}

	g := graph.NewMemoryGraph()
	for _, t := range pending {
		_ = g.AddNode(ctx, core.Node{ID: t, Type: core.NodeAsset, Attrs: map[string]string{"asset": t}})
	}

	p := probe.NewProber()
	p.Threads = o.threads
	if profile == core.ProfileStealth {
		p.Threads = min(p.Threads, 5)
	}
	if profile == core.ProfileAggressive {
		p.Threads = max(p.Threads, 50)
	}

	// ---- Phase 1: probe (single httpx pool), stream provisional ----
	var (
		corpusMu sync.Mutex
		corpus   []core.HTTPResponse
	)
	log.Printf("phase-1: probing %d hosts", len(pending))
	probed, err := p.Probe(ctx, pending, func(hr core.HTTPResponse) {
		corpusMu.Lock()
		corpus = append(corpus, hr)
		corpusMu.Unlock()
		// Provisional heuristic: rarity unknown yet, so score 0 + evidence.
		_ = out.Provisional(hr.Asset, 0, 0.2, []string{
			fmt.Sprintf("status=%d", hr.StatusCode),
			"provisional: rerank pending",
		})
		// Graph enrichment (bounded metadata only).
		_ = g.AddNode(ctx, core.Node{ID: hr.Asset, Type: core.NodeAsset, Attrs: map[string]string{"title": hr.Title}})
		for _, ip := range hr.IPs {
			_ = g.AddNode(ctx, core.Node{ID: "ip:" + ip, Type: core.NodeInfrastructure})
			_ = g.AddEdge(ctx, core.Edge{From: hr.Asset, To: "ip:" + ip, Type: core.EdgeResolvesTo})
		}
		for _, san := range hr.CertSANs {
			_ = g.AddNode(ctx, core.Node{ID: "cert:" + san, Type: core.NodeIdentity})
			_ = g.AddEdge(ctx, core.Edge{From: hr.Asset, To: "cert:" + san, Type: core.EdgeSharesCert})
		}
		store.MarkDone(camp, hr.Asset, "probe")
	})
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("probe: %w", err)
	}
	// httpx only returns successes via OnResult; assets with no response
	// still count as attempted so resume does not spin forever.
	for _, t := range pending {
		store.MarkDone(camp, t, "probe")
	}
	_ = store.Flush()
	log.Printf("phase-1: got %d responses", len(probed))

	if len(corpus) == 0 {
		log.Printf("no live hosts; nothing to score")
		return persistGraph(ctx, log, store, camp, g)
	}

	// ---- Phase 2: TF-IDF rerank + graph propagation + gated LLM ----
	engine, err := triage.NewEngine(ctx, corpus)
	if err != nil {
		return fmt.Errorf("triage: %w", err)
	}

	var llmClient *llm.Client
	if o.ollamaURL != "" && profile != core.ProfileStealth {
		llmClient = llm.NewClient(o.ollamaURL, o.ollamaModel, 4, 30*time.Second)
	} else {
		log.Printf("llm disabled (stealth or empty --ollama)")
	}

	type scored struct {
		res core.TriageResult
		hr  core.HTTPResponse
	}
	var (
		scoredMu sync.Mutex
		results  []scored
		byAsset  = map[string]core.HTTPResponse{}
	)
	for _, hr := range corpus {
		byAsset[hr.Asset] = hr
	}

	ex := dag.New(o.workers, o.workers*16, func(taskCtx context.Context, task core.Task) error {
		hr, ok := byAsset[task.Asset]
		if !ok {
			return nil
		}
		res, err := engine.Score(taskCtx, core.Asset{Name: task.Asset}, g)
		if err != nil {
			return err
		}
		// Ambiguous/medium band → LLM (bounded pool, always degrades cleanly).
		// Verdict→score policy lives in llm.ApplyVerdict (unit-tested).
		if llmClient != nil && triage.InAmbiguityBand(res.FinalScore) {
			class, _ := llmClient.ClassifyAmbiguous(taskCtx, hr)
			res = llm.ApplyVerdict(res, class)
		}
		scoredMu.Lock()
		results = append(results, scored{res: res, hr: hr})
		scoredMu.Unlock()
		store.MarkDone(camp, task.Asset, "score")
		return nil
	})
	ex.SetProfile(profile)

	// Submit must not block forever on cancel: use a feeder goroutine + CloseQueue.
	go func() {
		defer ex.CloseQueue()
		for _, hr := range corpus {
			if err := ex.Submit(ctx, core.Task{Stage: "score", Asset: hr.Asset}); err != nil {
				return
			}
		}
	}()
	if err := ex.Run(ctx); err != nil && ctx.Err() == nil {
		return fmt.Errorf("dag: %w", err)
	}
	_ = store.Flush()

	// Graph propagation: interest flows from high-score nodes (BFS, capped).
	for _, s := range results {
		if s.res.FinalScore >= triage.AmbiguityHi {
			_ = g.PropagateScore(ctx, s.res.Asset, s.res.FinalScore, 0.5)
		}
	}

	// Deterministic final output: score desc, asset asc tiebreak.
	sort.Slice(results, func(i, j int) bool {
		if results[i].res.FinalScore != results[j].res.FinalScore {
			return results[i].res.FinalScore > results[j].res.FinalScore
		}
		return results[i].res.Asset < results[j].res.Asset
	})
	// Representative dedupe: emit cluster heads, count members.
	finalByAsset := make(map[string]core.TriageResult, len(results))
	emitted := map[string]bool{}
	for i := range results {
		s := &results[i]
		// Enrich the verdict with probe context before it hits stdout,
		// so the persisted blob and the JSONL line carry tech/title/status.
		s.res.Tech = append([]string{}, s.hr.Tech...)
		s.res.Status = s.hr.StatusCode
		s.res.Title = s.hr.Title
		finalByAsset[s.res.Asset] = s.res
		if emitted[s.res.ClusterID] {
			continue
		}
		emitted[s.res.ClusterID] = true
		if err := out.Emit(s.res); err != nil {
			return fmt.Errorf("stdout: %w", err)
		}
	}

	// Persist every verdict (not just cluster heads) for filter/ui.
	rows := make([]state.ResultRow, 0, len(results))
	for _, s := range results {
		rows = append(rows, state.ResultRow{
			Result: s.res, Status: s.hr.StatusCode,
			Title: s.hr.Title, Tech: s.hr.Tech, Headers: s.hr.Headers,
		})
	}
	if err := store.SaveResults(ctx, camp, rows); err != nil {
		return fmt.Errorf("results persist: %w", err)
	}
	log.Printf("results: persisted %d verdicts for campaign %q", len(rows), camp)

	if err := persistGraph(ctx, log, store, camp, g); err != nil {
		return err
	}

	// Live-fire corpus dump for offline scorer/LLM tuning.
	if o.exportCorpus != "" {
		dump := engine.DumpCorpus(camp, finalByAsset)
		f, err := os.Create(o.exportCorpus)
		if err != nil {
			return fmt.Errorf("export-corpus: %w", err)
		}
		werr := triage.WriteCorpusDump(ctx, f, dump)
		cerr := f.Close()
		if werr != nil {
			return fmt.Errorf("export-corpus: %w", werr)
		}
		if cerr != nil {
			return fmt.Errorf("export-corpus: %w", cerr)
		}
		log.Printf("corpus: wrote %d docs + %d df terms to %s", dump.NumDocs, len(dump.DF), o.exportCorpus)
	}

	// Nuclei Top-N post-processing: exposure/misconfig templates only,
	// gated by triage score, hard-bounded by timeout. Any failure degrades
	// to zero findings — the pipeline always completes.
	if o.nucleiEnable {
		runNucleiPostStep(ctx, log, out, store, camp, o)
	}
	log.Printf("phase-2: scored=%d clusters=%d", len(results), len(emitted))
	return nil
}

// runNucleiPostStep queries the persisted Top-N, scans, streams findings as
// {"type":"nuclei_finding",...} JSONL, and stores them for filter/ui.
// It never returns an error: engine init, template, network, and timeout
// failures all degrade to a stderr warning.
func runNucleiPostStep(ctx context.Context, log interface{ Printf(string, ...any) }, out *output.Writer, store *state.Store, camp string, o *scanOpts) {
	rows, err := store.QueryResults(ctx, camp, state.FilterParams{
		MinScore: o.nucleiMinScore, Limit: o.nucleiMaxHosts,
	})
	if err != nil {
		log.Printf("nuclei: top-N query failed, skipping: %v", err)
		return
	}
	if len(rows) == 0 {
		log.Printf("nuclei: no hosts above %.2f, skipping", o.nucleiMinScore)
		return
	}
	targets := make([]nuclei.Target, 0, len(rows))
	for _, r := range rows {
		targets = append(targets, nuclei.Target{Asset: r.Result.Asset, Score: r.Result.FinalScore})
	}
	runner := nuclei.New(nuclei.Options{
		Tags: o.nucleiTags, ExcludeTags: o.nucleiExTags,
		Concurrency: 10, Timeout: o.nucleiTimeout,
		TemplatesDir: o.nucleiTplDir,
	})
	var (
		mu     sync.Mutex
		stored []state.StoredFinding
	)
	n, err := runner.Run(ctx, targets, func(f nuclei.Finding) {
		_ = out.EmitFinding(f)
		mu.Lock()
		stored = append(stored, state.StoredFinding{
			Asset: f.Asset, TemplateID: f.TemplateID, Severity: f.Severity,
			Name: f.Name, Matched: f.Matched, URL: f.URL,
		})
		mu.Unlock()
	})
	if err != nil {
		log.Printf("nuclei: degraded after %d findings: %v", n, err)
	}
	if err := store.SaveFindings(ctx, camp, stored); err != nil {
		log.Printf("nuclei: findings persist failed: %v", err)
	}
	log.Printf("nuclei: scanned=%d findings=%d tags=%v", len(targets), n, o.nucleiTags)
}

// persistGraph snapshots the in-memory graph into SQLite for `rarefy export`.
func persistGraph(ctx context.Context, log interface{ Printf(string, ...any) }, store *state.Store, camp string, g *graph.MemoryGraph) error {
	nodes, edges, err := g.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("graph snapshot: %w", err)
	}
	if err := store.SaveGraph(ctx, camp, nodes, edges); err != nil {
		return fmt.Errorf("graph persist: %w", err)
	}
	log.Printf("graph: persisted %d nodes %d edges for campaign %q", len(nodes), len(edges), camp)
	return nil
}

func loadTargets(domain, listFile string, args []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		// BYOS lists tolerate comments, blank lines, raw URLs, ports,
		// query strings, and trailing dots (amass/subfinder paste-friendly).
		s = strings.TrimSpace(s)
		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "//") {
			return
		}
		if i := strings.Index(s, " #"); i >= 0 {
			s = strings.TrimSpace(s[:i])
		}
		s = strings.ToLower(s)
		s = strings.TrimPrefix(s, "http://")
		s = strings.TrimPrefix(s, "https://")
		if i := strings.Index(s, "/"); i >= 0 {
			s = s[:i]
		}
		if i := strings.Index(s, "?"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSuffix(s, ".")
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	if domain != "" {
		for _, d := range strings.Split(domain, ",") {
			add(d)
		}
	}
	if listFile != "" {
		f, err := os.Open(listFile)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			add(sc.Text())
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	for _, a := range args {
		add(a)
	}
	if listFile == "" && len(args) == 0 && domain == "" {
		if fi, _ := os.Stdin.Stat(); fi != nil && (fi.Mode()&os.ModeCharDevice) == 0 {
			sc := bufio.NewScanner(os.Stdin)
			sc.Buffer(make([]byte, 64*1024), 1024*1024)
			for sc.Scan() {
				add(sc.Text())
			}
			if err := sc.Err(); err != nil {
				return nil, err
			}
		}
	}
	sort.Strings(out)
	return out, nil
}
