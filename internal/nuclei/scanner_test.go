package nuclei

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/projectdiscovery/nuclei/v3/pkg/model/types/severity"
	"github.com/projectdiscovery/nuclei/v3/pkg/model/types/stringslice"
	"github.com/projectdiscovery/nuclei/v3/pkg/output"
)

// testOptions points the runner at a fixture bundle so the preflight
// passes without touching the real ~/.nuclei checkout.
func testOptions(t *testing.T) Options {
	t.Helper()
	dir := t.TempDir()
	dummy := "id: dummy\ninfo:\n  name: dummy\n  severity: info\n  tags: exposure\n"
	if err := os.WriteFile(filepath.Join(dir, "dummy.yaml"), []byte(dummy), 0600); err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions()
	opts.TemplatesDir = dir
	return opts
}

// fakeEngine replays scripted events without templates or network.
type fakeEngine struct {
	cb     func(*output.ResultEvent)
	events []*output.ResultEvent
	err    error
	block  chan struct{} // if non-nil, Execute blocks until ctx done
}

func (f *fakeEngine) LoadTargets(_ []string, _ bool) {}
func (f *fakeEngine) Close()                         {}
func (f *fakeEngine) ExecuteCallbackWithCtx(ctx context.Context, cbs ...func(*output.ResultEvent)) error {
	if f.block != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-f.block:
		}
	}
	all := append([]func(*output.ResultEvent){f.cb}, cbs...)
	for _, ev := range f.events {
		for _, cb := range all {
			if cb != nil {
				cb(ev)
			}
		}
	}
	return f.err
}

func testEvent() *output.ResultEvent {
	ev := &output.ResultEvent{
		TemplateID: "exposed-git-config",
		Host:       "admin.t",
		Matched:    "https://admin.t/.git/config",
		URL:        "https://admin.t/.git/config",
	}
	ev.Info.Name = "Git Config Exposure"
	ev.Info.SeverityHolder.Severity = severity.High
	ev.Info.Tags = stringslice.New([]string{"exposure", "config"})
	return ev
}

func TestMapEvent(t *testing.T) {
	f := mapEvent(testEvent(), map[string]string{"admin.t": "admin.t"})
	if f.Type != "nuclei_finding" || f.Asset != "admin.t" ||
		f.TemplateID != "exposed-git-config" || f.Severity != "high" ||
		f.Name != "Git Config Exposure" || len(f.Tags) != 2 {
		t.Fatalf("bad mapping: %+v", f)
	}
	raw, _ := json.Marshal(f)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if m["type"] != "nuclei_finding" {
		t.Fatalf("missing discriminator: %s", raw)
	}
}

func TestRunStreamsFindings(t *testing.T) {
	r := New(testOptions(t))
	r.newEngine = func(_ context.Context, cb func(*output.ResultEvent)) (engine, error) {
		return &fakeEngine{cb: cb, events: []*output.ResultEvent{testEvent(), testEvent()}}, nil
	}
	var got []Finding
	n, err := r.Run(context.Background(), []Target{{Asset: "admin.t", Score: 0.9}}, func(f Finding) {
		got = append(got, f)
	})
	if err != nil || n != 2 || len(got) != 2 {
		t.Fatalf("n=%d err=%v got=%d", n, err, len(got))
	}
}

func TestRunEmptyTargetsSkipsEngine(t *testing.T) {
	r := New(testOptions(t))
	called := false
	r.newEngine = func(_ context.Context, _ func(*output.ResultEvent)) (engine, error) {
		called = true
		return &fakeEngine{}, nil
	}
	n, err := r.Run(context.Background(), nil, func(Finding) {})
	if err != nil || n != 0 || called {
		t.Fatalf("n=%d err=%v called=%v", n, err, called)
	}
}

func TestRunEngineErrorDegrades(t *testing.T) {
	r := New(testOptions(t))
	r.newEngine = func(_ context.Context, _ func(*output.ResultEvent)) (engine, error) {
		return nil, errors.New("no templates: offline")
	}
	n, err := r.Run(context.Background(), []Target{{Asset: "a.t"}}, func(Finding) {})
	if err == nil || n != 0 {
		t.Fatalf("expected setup error, got n=%d err=%v", n, err)
	}
}

func TestRunTimeoutIsBoundaryNotFailure(t *testing.T) {
	opts := testOptions(t)
	opts.Timeout = 50 * time.Millisecond
	r := New(opts)
	r.newEngine = func(_ context.Context, _ func(*output.ResultEvent)) (engine, error) {
		return &fakeEngine{block: make(chan struct{})}, nil
	}
	start := time.Now()
	n, err := r.Run(context.Background(), []Target{{Asset: "a.t"}}, func(Finding) {})
	if err != nil || n != 0 {
		t.Fatalf("timeout must degrade cleanly, got n=%d err=%v", n, err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("timeout not enforced")
	}
}

func TestDefaultsAreBountySafe(t *testing.T) {
	o := DefaultOptions()
	if len(o.Tags) == 0 || o.Timeout <= 0 || o.Concurrency <= 0 {
		t.Fatalf("unsafe defaults: %+v", o)
	}
	for _, tag := range o.Tags {
		if tag == "cve" || tag == "intrusive" || tag == "dos" {
			t.Fatalf("aggressive tag in defaults: %q", tag)
		}
	}
	for _, want := range []string{"intrusive", "dos", "oast"} {
		found := false
		for _, tag := range o.ExcludeTags {
			if tag == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("default excludes missing %q: %v", want, o.ExcludeTags)
		}
	}
}

func TestPreflightMissingBundleSkipsEngine(t *testing.T) {
	opts := DefaultOptions()
	opts.TemplatesDir = filepath.Join(t.TempDir(), "does-not-exist")
	r := New(opts)
	called := false
	r.newEngine = func(_ context.Context, _ func(*output.ResultEvent)) (engine, error) {
		called = true
		return &fakeEngine{}, nil
	}
	n, err := r.Run(context.Background(), []Target{{Asset: "a.t"}}, func(Finding) {})
	if err == nil || n != 0 || called {
		t.Fatalf("missing bundle must skip engine, got n=%d err=%v called=%v", n, err, called)
	}
	if !strings.Contains(err.Error(), "--nuclei-templates") {
		t.Fatalf("error must be actionable, got: %v", err)
	}
}

func TestPreflightEmptyDirSkipsEngine(t *testing.T) {
	opts := DefaultOptions()
	opts.TemplatesDir = t.TempDir() // exists, no templates
	r := New(opts)
	if _, err := r.Run(context.Background(), []Target{{Asset: "a.t"}}, func(Finding) {}); err == nil {
		t.Fatal("empty bundle dir must fail preflight")
	}
}
