// Package cli — update.go: latest-release notice for bare invocations.
//
// The check runs ONLY when `rarefy` is invoked with no subcommand, so
// pipelines (scan/filter/...) never pay a network round-trip and never
// see advisory text on stdout. Results are cached for 24h; offline
// failures degrade to silence.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	updateRepo       = "raghavraut/rarefy"
	updateAPI        = "https://api.github.com/repos/raghavraut/rarefy/releases/latest"
	updateTTL        = 24 * time.Hour
	updateTimeout    = 3 * time.Second
	updateInstallCmd = "go install -v github.com/raghavraut/rarefy/cmd/rarefy@latest"
)

type cachedTag struct {
	Tag       string `json:"tag"`
	CheckedAt int64  `json:"checked_at"`
}

func updateCacheFile() string {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "rarefy", "latest.json")
}

// fetchLatestTag asks GitHub for the newest release tag.
func fetchLatestTag(apiURL string, timeout time.Duration) (string, error) {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github api: %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", err
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", err
	}
	if body.TagName == "" {
		return "", fmt.Errorf("github api: empty tag_name")
	}
	return body.TagName, nil
}

// latestCached returns the newest known release tag, refreshing from the
// network when the cache is older than updateTTL. Empty string on any
// failure (caller stays silent).
func latestCached() string {
	path := updateCacheFile()
	if path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			var c cachedTag
			if json.Unmarshal(raw, &c) == nil && c.Tag != "" {
				if time.Since(time.Unix(c.CheckedAt, 0)) < updateTTL {
					return c.Tag
				}
			}
		}
	}
	tag, err := fetchLatestTag(updateAPI, updateTimeout)
	if err != nil {
		return ""
	}
	if path != "" {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		raw, _ := json.Marshal(cachedTag{Tag: tag, CheckedAt: time.Now().Unix()})
		_ = os.WriteFile(path, raw, 0o644)
	}
	return tag
}

// versionStatus classifies the build stamp against the latest tag.
// Returns "current", "ahead" (post-release dev build), "behind", or
// "unknown" (dev/local builds that carry no version).
func versionStatus(current, latest string) string {
	cur := normalizeTag(current)
	lat := normalizeTag(latest)
	if lat == "" || cur == "" {
		return "unknown"
	}
	if cur == lat {
		return "current"
	}
	curBase, _ := splitSuffix(cur)
	latBase, _ := splitSuffix(lat)
	if curBase != latBase {
		if compareDots(curBase, latBase) > 0 {
			return "ahead"
		}
		return "behind"
	}
	// Same base, different suffix: a dev build past its tag is ahead.
	return "ahead"
}

// splitSuffix cuts "1.0.1-0.2026-abc" into ("1.0.1", "-0.2026-abc").
func splitSuffix(v string) (base, suffix string) {
	if i := strings.Index(v, "-"); i >= 0 {
		return v[:i], v[i:]
	}
	return v, ""
}

// compareDots compares dot-separated numeric versions ("2" < "10").
// Non-numeric segments compare as zero; missing segments are zero.
func compareDots(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var av, bv int
		if i < len(as) {
			fmt.Sscanf(as[i], "%d", &av)
		}
		if i < len(bs) {
			fmt.Sscanf(bs[i], "%d", &bv)
		}
		if av != bv {
			if av > bv {
				return 1
			}
			return -1
		}
	}
	return 0
}

// normalizeTag reduces "v1.0.0 (abc 2026-..)", "v1.0.0", "dev (local
// build)" etc. to a bare "1.0.0" (or "" when there is no version).
func normalizeTag(v string) string {
	fields := strings.Fields(strings.TrimSpace(v))
	if len(fields) == 0 {
		return ""
	}
	tok := strings.TrimPrefix(fields[0], "v")
	if tok == "" || tok == "dev" || tok == "(devel)" {
		return ""
	}
	return tok
}

// printVersionNotice writes the update nudge. Silent when offline,
// unparseable, current, or ahead — it must never nag wrongly.
func printVersionNotice(w io.Writer, buildVersion string) {
	latest := latestCached()
	if latest == "" {
		return
	}
	switch versionStatus(buildVersion, latest) {
	case "current", "ahead":
		fmt.Fprintf(w, "rarefy is up to date (%s).\n", latest)
	case "behind", "unknown":
		fmt.Fprintf(w, "A newer rarefy is available: %s (you have %s).\nUpdate with: %s\n",
			latest, strings.TrimSpace(buildVersion), updateInstallCmd)
	}
}
