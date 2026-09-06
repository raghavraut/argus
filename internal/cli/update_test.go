package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVersionStatus(t *testing.T) {
	cases := []struct {
		current, latest, want string
	}{
		{"v1.0.0 (abc 2026-01-01)", "v1.0.0", "current"},
		{"1.0.0", "v1.0.0", "current"},
		{"v1.0.0", "v1.0.0", "current"},
		{"v0.9.0", "v1.0.0", "behind"},
		{"v1.0.1-0.20260906-072973c", "v1.0.1", "ahead"},
		{"v1.0.1-0.20260906-072973c", "v1.0.0", "ahead"},
		{"v1.0.0-0.20260906-abc1234", "v1.0.0", "ahead"},
		{"v2.0.0", "v10.0.0", "behind"},
		{"dev (local build)", "v1.0.0", "unknown"},
		{"dev (none unknown)", "v1.0.0", "unknown"},
		{"", "v1.0.0", "unknown"},
		{"v1.0.0", "", "unknown"},
	}
	for _, c := range cases {
		if got := versionStatus(c.current, c.latest); got != c.want {
			t.Fatalf("versionStatus(%q,%q)=%q want %q", c.current, c.latest, got, c.want)
		}
	}
}

func TestFetchLatestTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == "" {
			t.Error("missing Accept header")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": "v9.9.9"})
	}))
	defer srv.Close()
	got, err := fetchLatestTag(srv.URL, 5*time.Second)
	if err != nil || got != "v9.9.9" {
		t.Fatalf("got %q err %v", got, err)
	}
	if _, err := fetchLatestTag("http://127.0.0.1:1/", time.Second); err == nil {
		t.Fatal("expected error for dead server")
	}
}

func TestBareRunIsTerseError(t *testing.T) {
	// Point the cache at an empty dir so no network happens and no
	// notice prints: the assertion is purely about the error shape.
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := NewRoot()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "missing command") {
		t.Fatalf("expected missing-command error, got %v", err)
	}
	if strings.Contains(errOut.String(), "Usage:") {
		t.Fatalf("bare run must not dump the guide:\n%s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "rarefy --help") {
		t.Fatalf("bare run must point at --help:\n%s", errOut.String())
	}
}

func seedFreshCache(t *testing.T, tag string) {
	t.Helper()
	path := updateCacheFile()
	if path == "" {
		t.Fatal("no cache path")
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	raw, _ := json.Marshal(cachedTag{Tag: tag, CheckedAt: time.Now().Unix()})
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPrintVersionNotice(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)
	t.Setenv("XDG_CACHE_HOME", dir)
	seedFreshCache(t, "v9.9.9")

	var behind bytes.Buffer
	printVersionNotice(&behind, "v0.1.0")
	if !strings.Contains(behind.String(), "v9.9.9") ||
		!strings.Contains(behind.String(), "go install -v") {
		t.Fatalf("behind notice wrong:\n%s", behind.String())
	}
	var current bytes.Buffer
	printVersionNotice(&current, "v9.9.9")
	if !strings.Contains(current.String(), "up to date") {
		t.Fatalf("current notice wrong:\n%s", current.String())
	}
}

func TestCachedTagRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)
	t.Setenv("XDG_CACHE_HOME", dir)
	path := updateCacheFile()
	if path == "" || !strings.HasSuffix(path, "latest.json") {
		t.Fatalf("bad cache path %q", path)
	}
	// Fresh cache hit: no network, returns stored tag.
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	raw, _ := json.Marshal(cachedTag{Tag: "v9.9.9", CheckedAt: time.Now().Unix()})
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := latestCached(); got != "v9.9.9" {
		t.Fatalf("cache miss, got %q", got)
	}
	// Stale cache + dead network = silent empty (never nag wrongly).
	raw, _ = json.Marshal(cachedTag{Tag: "v0.0.1", CheckedAt: time.Now().Add(-48 * time.Hour).Unix()})
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = raw
}
