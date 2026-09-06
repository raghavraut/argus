// Package llm provides a bounded Ollama client with strict graceful degradation.
//
// Pool fix: all classifications flow through a fixed set of workers fed by a
// bounded channel. If Ollama is down, slow, or misconfigured, ClassifyAmbiguous
// returns a degraded unknown verdict instead of failing the pipeline.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/raghavraut/rarefy/internal/core"
)

const (
	defaultModel   = "llama3.1:8b"
	defaultTimeout = 30 * time.Second
	maxPromptChars = 4000
)

// Client is a bounded Ollama classifier implementing core.SemanticAnalyzer.
type Client struct {
	baseURL string
	model   string
	http    *http.Client

	queue   chan classifyReq
	workers int
	once    sync.Once
}

type classifyReq struct {
	resp core.HTTPResponse
	out  chan classifyRes
}

type classifyRes struct {
	class core.LLMClassification
	err   error
}

var _ core.SemanticAnalyzer = (*Client)(nil)

// NewClient creates a classifier. workers<=0 defaults to 4.
func NewClient(baseURL, model string, workers int, timeout time.Duration) *Client {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if model == "" {
		model = defaultModel
	}
	if workers <= 0 {
		workers = 4
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		http:    &http.Client{Timeout: timeout},
		queue:   make(chan classifyReq, 64),
		workers: workers,
	}
}

func (c *Client) start() {
	c.once.Do(func() {
		for i := 0; i < c.workers; i++ {
			go c.worker()
		}
	})
}

func (c *Client) worker() {
	for req := range c.queue {
		class, err := c.call(context.Background(), req.resp)
		if err != nil {
			class = degraded(err.Error())
		}
		req.out <- classifyRes{class: class, err: nil}
	}
}

// ClassifyAmbiguous strips boilerplate, enqueues with caller-context
// cancellation, and never returns an error — degradation is encoded.
func (c *Client) ClassifyAmbiguous(ctx context.Context, resp core.HTTPResponse) (core.LLMClassification, error) {
	c.start()
	out := make(chan classifyRes, 1)
	select {
	case <-ctx.Done():
		return degraded("cancelled"), nil
	case c.queue <- classifyReq{resp: resp, out: out}:
	}
	select {
	case <-ctx.Done():
		return degraded("cancelled"), nil
	case r := <-out:
		return r.class, nil
	}
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Stream   bool      `json:"stream"`
	Format   string    `json:"format,omitempty"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Message message `json:"message"`
	Done    bool    `json:"done"`
	Error   string  `json:"error,omitempty"`
}

func (c *Client) call(ctx context.Context, resp core.HTTPResponse) (core.LLMClassification, error) {
	prompt := buildPrompt(resp)
	body, err := json.Marshal(chatRequest{
		Model: c.model,
		Messages: []message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: prompt},
		},
		Stream: false,
	})
	if err != nil {
		return core.LLMClassification{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return core.LLMClassification{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	httpResp, err := c.http.Do(req)
	if err != nil {
		return core.LLMClassification{}, err
	}
	defer func() { _ = httpResp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(httpResp.Body, 16*1024))
	if err != nil {
		return core.LLMClassification{}, err
	}
	if httpResp.StatusCode >= 300 {
		return core.LLMClassification{}, fmt.Errorf("ollama status %d", httpResp.StatusCode)
	}
	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return core.LLMClassification{}, err
	}
	if cr.Error != "" {
		return core.LLMClassification{}, fmt.Errorf("ollama: %s", cr.Error)
	}
	var verdict struct {
		Classification string  `json:"classification"`
		Confidence     float64 `json:"confidence"`
		Reason         string  `json:"reason"`
	}
	content := strings.TrimSpace(cr.Message.Content)
	if err := json.Unmarshal([]byte(content), &verdict); err != nil {
		// Model wrapped JSON in prose — degrade rather than fail.
		return core.LLMClassification{
			Classification: "UNKNOWN", Confidence: 0.2,
			Reason: "unparseable model output", Degraded: true,
		}, nil
	}
	if verdict.Confidence < 0 {
		verdict.Confidence = 0
	}
	if verdict.Confidence > 1 {
		verdict.Confidence = 1
	}
	return core.LLMClassification{
		Classification: verdict.Classification,
		Confidence:     verdict.Confidence,
		Reason:         verdict.Reason,
	}, nil
}

// systemPrompt is the JSON-enforced AppSec triage contract. The taxonomy is
// deliberately closed: models must pick one label so ApplyVerdict can map it
// deterministically. Parsing is case-insensitive for robustness.
const systemPrompt = `You are an expert AppSec engineer performing recon triage. ` +
	`Analyze this HTTP response (headers + truncated body) and classify it as exactly one of: ` +
	`WAF_BLOCK (CDN/WAF block or challenge page), PARKED (domain parking / for-sale page), ` +
	`GENERIC_ERROR (default server error, stackless 404/500, empty placeholder), ` +
	`UNIQUE_APP (custom application surface worth manual review), ` +
	`ADMIN_PANEL (login or administrative interface). ` +
	`Return strict JSON only, no prose: {"classification": string, "confidence": float 0.0-1.0, "reason": string}.`

func buildPrompt(resp core.HTTPResponse) string {
	var b strings.Builder
	b.WriteString("Analyze this HTTP response. Classify it as WAF_BLOCK, PARKED, GENERIC_ERROR, UNIQUE_APP, or ADMIN_PANEL. Return strict JSON.\n")
	b.WriteString("Asset: " + resp.Asset + "\n")
	b.WriteString(fmt.Sprintf("Status: %d\n", resp.StatusCode))
	b.WriteString("Title: " + resp.Title + "\n")
	if len(resp.Headers) > 0 {
		b.WriteString("Headers:\n")
		for k, v := range resp.Headers {
			b.WriteString("  " + k + ": " + v + "\n")
		}
	}
	text := resp.BodyPreview
	if len(text) > maxPromptChars {
		text = text[:maxPromptChars]
	}
	b.WriteString("Body:\n" + text + "\n")
	s := b.String()
	if len(s) > maxPromptChars+1024 {
		s = s[:maxPromptChars+1024]
	}
	return s
}

func degraded(reason string) core.LLMClassification {
	return core.LLMClassification{
		Classification: "UNKNOWN", Confidence: 0.2,
		Reason: "degraded: " + reason, Degraded: true,
	}
}
