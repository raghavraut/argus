// Package probe wraps ProjectDiscovery engines as Go libraries (no os/exec).
//
// Concurrency fix: a single httpx Runner owns its internal worker pool.
// Argus never wraps it in a second pool — targets are handed to httpx once
// via InputTargetHost and results return through the OnResult callback.
package probe

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/projectdiscovery/dnsx/libs/dnsx"
	"github.com/projectdiscovery/goflags"
	"github.com/projectdiscovery/httpx/runner"

	"github.com/raghavraut/argus/internal/core"
	"github.com/raghavraut/argus/internal/triage"
)

const (
	maxBodyPreview       = 4096
	maxResponseReadBytes = 512 * 1024
)

// Prober holds httpx/dnsx configuration. Runner instances are short-lived
// per Probe call (one internal pool per call), never per asset.
type Prober struct {
	Threads int
	Timeout int
	Retries int
}

// NewProber returns defaults (25 threads, 10s timeout, 1 retry).
func NewProber() *Prober {
	return &Prober{Threads: 25, Timeout: 10, Retries: 1}
}

// Resolve uses dnsx as a library to map asset -> IPs (NXDOMAIN filtered by error).
func Resolve(asset string) ([]string, error) {
	client, err := dnsx.New(dnsx.DefaultOptions)
	if err != nil {
		return nil, err
	}
	ips, err := client.Lookup(asset)
	if err != nil {
		return nil, err
	}
	return ips, nil
}

// Probe runs httpx once over all targets and maps results to core.HTTPResponse.
// onEach, if non-nil, receives provisional results for Phase-1 streaming.
func (p *Prober) Probe(ctx context.Context, targets []string, onEach func(core.HTTPResponse)) ([]core.HTTPResponse, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	threads := p.Threads
	if threads <= 0 {
		threads = 25
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 10
	}

	var (
		mu  sync.Mutex
		out []core.HTTPResponse
	)

	options := runner.Options{
		Methods:                  "GET",
		InputTargetHost:          goflags.StringSlice(targets),
		Threads:                  threads,
		Timeout:                  timeout,
		Retries:                  p.Retries,
		ExtractTitle:             true,
		StatusCode:               true,
		ContentLength:            true,
		OutputIP:                 true,
		OutputCName:              true,
		Favicon:                  true,
		TechDetect:               true,
		ResponseBodyPreviewSize:  100,
		MaxResponseBodySizeToRead: maxResponseReadBytes,
		DisableUpdateCheck:       true,
		Silent:                   true,
		NoColor:                  true,
		// Critical for the Unix contract: httpx prints its own human-readable
		// line per result via gologger (stdout). We consume OnResult instead,
		// so suppress all runner-owned stdout; only our JSONL may use it.
		DisableStdout: true,
		OnResult: func(r runner.Result) {
			if r.Err != nil {
				return
			}
			hr := mapResult(r)
			mu.Lock()
			out = append(out, hr)
			mu.Unlock()
			if onEach != nil {
				onEach(hr)
			}
		},
	}
	if err := options.ValidateOptions(); err != nil {
		return nil, fmt.Errorf("httpx options: %w", err)
	}
	hr, err := runner.New(&options)
	if err != nil {
		return nil, fmt.Errorf("httpx runner: %w", err)
	}
	defer hr.Close()

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			hr.Interrupt()
		case <-done:
		}
	}()
	hr.RunEnumeration()
	close(done)

	mu.Lock()
	defer mu.Unlock()
	return out, ctx.Err()
}

func mapResult(r runner.Result) core.HTTPResponse {
	preview := r.BodyPreview
	if len(preview) > maxBodyPreview {
		preview = preview[:maxBodyPreview]
	}
	sum := md5.Sum([]byte(r.ResponseBody))
	hr := core.HTTPResponse{
		Asset:       r.Input,
		StatusCode:  r.StatusCode,
		Title:       r.Title,
		Headers:     flattenHeaders(r.ResponseHeaders),
		BodyPreview: preview,
		BodyMD5:     hex.EncodeToString(sum[:]),
		SimHash:     triage.SimHash64(r.Title + " " + preview),
		FaviconHash: r.FavIconMMH3,
		CDN:         r.CDNName,
		Tech:        append([]string{}, r.Technologies...),
		IPs:         append(append([]string{}, r.A...), r.AAAA...),
	}
	if r.TLSData != nil && len(r.TLSData.SubjectAN) > 0 {
		hr.CertSANs = append([]string{}, r.TLSData.SubjectAN...)
	}
	hr.TokenCounts = triage.Tokenize(hr)
	t := 0
	for _, c := range hr.TokenCounts {
		t += c
	}
	hr.TotalTokens = t
	return hr
}

func flattenHeaders(in map[string]interface{}) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		switch t := v.(type) {
		case string:
			out[strings.ToLower(k)] = t
		case []string:
			out[strings.ToLower(k)] = strings.Join(t, " ")
		default:
			out[strings.ToLower(k)] = strings.TrimSpace(strings.ReplaceAll(strings.TrimPrefix(strings.TrimSuffix(strings.TrimSpace(keysOf(v)), "]"), "["), ",", " "))
		}
	}
	return out
}

func keysOf(v interface{}) string { return strings.TrimSpace(strings.Join([]string{strings.TrimSpace(strings.ReplaceAll(sprintf("%v", v), "\n", " "))}, "")) }

func sprintf(format string, a ...interface{}) string {
	// tiny fmt.Sprintf wrapper to keep imports obvious
	return fmt.Sprintf(format, a...)
}
