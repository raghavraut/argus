// Package nuclei runs exposure/misconfiguration templates over the
// TF-IDF Top-N as a post-processing step.
//
// Safety design (bug-bounty friendly):
//   - tag allowlist only (default exposure + misconfig), never full scans
//   - risky protocols excluded (headless, code, javascript, workflow)
//   - sandbox: no local file access, local network restricted
//   - no template auto-upgrade (hermetic, offline-safe init)
//   - strict context timeout: a hanging template can never deadlock Rarefy
//   - stdout contract: findings stream as {"type":"nuclei_finding",...} lines,
//     engine-owned output stays on the in-memory mock writer (never stdout)
package nuclei

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	nuclei "github.com/projectdiscovery/nuclei/v3/lib"
	"github.com/projectdiscovery/nuclei/v3/pkg/output"
)

// Defaults: Top-N gate and safe tag set.
const (
	DefaultMinScore = 0.7
	DefaultMaxHosts = 50
	DefaultTimeout  = 10 * time.Minute
)

// DefaultTags restricts scanning to passive-surface templates.
var DefaultTags = []string{"exposure", "misconfig"}

// DefaultExcludeTags drops templates that actively exploit, DoS, or phone
// out-of-band interactsh servers — even when they carry an allowlisted tag
// (e.g. openbmcs-ssrf is tagged misconfig+oast). QA live-fire caught 25
// intrusive + 3 oast findings firing at a live bounty target under the
// tag allowlist alone; the denylist closes that hole.
var DefaultExcludeTags = []string{"intrusive", "dos", "oast"}

// excludedProtocols keeps active/intrusive template protocols out.
var excludedProtocols = []string{"headless", "code", "javascript", "workflow"}

// Target is one gated asset.
type Target struct {
	Asset string
	Score float64
}

// Finding is one streamed result line.
type Finding struct {
	Type       string   `json:"type"` // always "nuclei_finding"
	Asset      string   `json:"asset"`
	TemplateID string   `json:"template-id"`
	Name       string   `json:"name"`
	Severity   string   `json:"severity"`
	Tags       []string `json:"tags"`
	Matched    string   `json:"matched"`
	URL        string   `json:"url"`
}

// Options tunes the post-processing run.
type Options struct {
	Tags         []string
	ExcludeTags  []string
	Concurrency  int
	Timeout      time.Duration
	TemplatesDir string // custom nuclei-templates checkout ("" = default locations)
}

// DefaultOptions returns the bounty-safe baseline.
func DefaultOptions() Options {
	return Options{
		Tags:        append([]string{}, DefaultTags...),
		ExcludeTags: append([]string{}, DefaultExcludeTags...),
		Concurrency: 10, Timeout: DefaultTimeout,
	}
}

// defaultTemplateDirs lists where the SDK looks for templates, most
// likely first. NOTE: nuclei's own default is %USERPROFILE%/nuclei-templates
// on Windows (per .templates-config.json), NOT under APPDATA — that path
// holds only config.yaml. Missing it here causes false "not installed"
// skips (caught by QA live-fire, v0.4).
func defaultTemplateDirs() []string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "windows" {
		dirs := []string{}
		if home != "" {
			dirs = append(dirs, filepath.Join(home, "nuclei-templates"))
		}
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			dirs = append(dirs, filepath.Join(appdata, "nuclei", "nuclei-templates"))
		}
		if home != "" {
			dirs = append(dirs, filepath.Join(home, ".nuclei", "nuclei-templates"))
		}
		return dirs
	}
	dirs := []string{}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, "nuclei-templates"))
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		dirs = append(dirs, filepath.Join(xdg, "nuclei", "nuclei-templates"))
	}
	if home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".config", "nuclei", "nuclei-templates"),
			filepath.Join(home, ".nuclei", "nuclei-templates"),
		)
	}
	return dirs
}

// hasTemplates reports whether dir holds at least one template file.
// Bounded walk: stops at the first .yaml/.yml or after 2000 entries.
func hasTemplates(dir string) bool {
	found := false
	count := 0
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || found || count > 2000 {
			if count > 2000 {
				return filepath.SkipAll
			}
			return nil
		}
		count++
		if !d.IsDir() {
			ext := strings.ToLower(filepath.Ext(d.Name()))
			if ext == ".yaml" || ext == ".yml" {
				found = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	return found
}

// resolveTemplates preflights template availability BEFORE engine init.
// Without this, a missing bundle triggers a multi-minute clone inside the
// scan that no context timeout reliably aborts. Returns the explicit source
// dir ("" = let the SDK use its defaults) or an actionable error.
func resolveTemplates(custom string) (string, error) {
	if custom != "" {
		if !hasTemplates(custom) {
			return "", fmt.Errorf("nuclei templates dir %q missing or has no templates (pass --nuclei-templates DIR with a nuclei-templates checkout)", custom)
		}
		return custom, nil
	}
	for _, dir := range defaultTemplateDirs() {
		if hasTemplates(dir) {
			return "", nil
		}
	}
	return "", fmt.Errorf("nuclei templates not installed: run `nuclei -update-templates` or pass --nuclei-templates DIR")
}

// engine is the subset of NucleiEngine we use (seam for tests).
type engine interface {
	LoadTargets(targets []string, probeNonHttp bool)
	ExecuteCallbackWithCtx(ctx context.Context, callback ...func(event *output.ResultEvent)) error
	Close()
}

// Runner executes one bounded Top-N run.
type Runner struct {
	opts      Options
	newEngine func(ctx context.Context, cb func(*output.ResultEvent)) (engine, error)
}

// New returns a Runner backed by the real SDK engine.
func New(opts Options) *Runner {
	if len(opts.Tags) == 0 {
		opts.Tags = append([]string{}, DefaultTags...)
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 10
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	r := &Runner{opts: opts}
	r.newEngine = r.realEngine
	return r
}

func (r *Runner) realEngine(ctx context.Context, cb func(*output.ResultEvent)) (engine, error) {
	opts := []nuclei.NucleiSDKOptions{
		nuclei.WithTemplateFilters(nuclei.TemplateFilters{
			Tags:                 r.opts.Tags,
			ExcludeTags:          r.opts.ExcludeTags,
			ExcludeProtocolTypes: strings.Join(excludedProtocols, ","),
		}),
		nuclei.WithResultCallback(cb),
		nuclei.WithTemplateUpdateCallback(true, func(string) {}),
		nuclei.WithSandboxOptions(false, true),
		nuclei.WithConcurrency(nuclei.Concurrency{
			TemplateConcurrency: 25, HostConcurrency: r.opts.Concurrency,
			HeadlessHostConcurrency: 1, HeadlessTemplateConcurrency: 1,
			JavascriptTemplateConcurrency: 1, TemplatePayloadConcurrency: 25,
			ProbeConcurrency: 50,
		}),
	}
	if r.opts.TemplatesDir != "" {
		opts = append(opts, nuclei.WithTemplatesOrWorkflows(nuclei.TemplateSources{
			Templates: []string{r.opts.TemplatesDir},
		}))
	}
	return nuclei.NewNucleiEngineCtx(ctx, opts...)
}

// Run scans targets and calls emit per finding (callbacks are concurrent).
// It returns the finding count. A timeout yields the partial count with a
// nil error (boundary, not failure); other errors are returned for the
// caller to log while the pipeline continues.
func (r *Runner) Run(ctx context.Context, targets []Target, emit func(Finding)) (int, error) {
	if len(targets) == 0 {
		return 0, nil
	}
	// Template preflight: never let engine init trigger a network clone.
	if _, err := resolveTemplates(r.opts.TemplatesDir); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.opts.Timeout)
	defer cancel()

	inputs := make([]string, 0, len(targets))
	assetOf := make(map[string]string, len(targets))
	for _, t := range targets {
		inputs = append(inputs, t.Asset)
		assetOf[t.Asset] = t.Asset
		assetOf["https://"+t.Asset] = t.Asset
		assetOf["http://"+t.Asset] = t.Asset
	}
	var (
		mu    sync.Mutex
		count int
	)
	ne, err := r.newEngine(ctx, func(ev *output.ResultEvent) {
		if ev == nil {
			return
		}
		f := mapEvent(ev, assetOf)
		mu.Lock()
		count++
		mu.Unlock()
		emit(f)
	})
	if err != nil {
		return 0, fmt.Errorf("nuclei engine: %w", err)
	}
	defer ne.Close()
	ne.LoadTargets(inputs, false)
	if err := ne.ExecuteCallbackWithCtx(ctx); err != nil {
		mu.Lock()
		n := count
		mu.Unlock()
		if ctx.Err() == context.DeadlineExceeded {
			return n, nil
		}
		return n, err
	}
	mu.Lock()
	defer mu.Unlock()
	return count, nil
}

// mapEvent flattens a ResultEvent into our stable Finding shape.
func mapEvent(ev *output.ResultEvent, assetOf map[string]string) Finding {
	asset := assetOf[strings.TrimSpace(ev.Host)]
	if asset == "" {
		asset = assetOf[strings.TrimSpace(ev.URL)]
	}
	if asset == "" {
		asset = strings.TrimSpace(ev.Host)
	}
	return Finding{
		Type: "nuclei_finding", Asset: asset,
		TemplateID: ev.TemplateID, Name: ev.Info.Name,
		Severity: ev.Info.SeverityHolder.Severity.String(),
		Tags:     append([]string{}, ev.Info.Tags.ToSlice()...),
		Matched:  ev.Matched, URL: ev.URL,
	}
}
