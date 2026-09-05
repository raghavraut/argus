// Package mockollama is a deterministic stand-in for a local Ollama server.
//
// CI has no GPU/RAM for Llama-class models, so LLM wiring tests (happy path
// AND graceful degradation) run against this mock instead of :11434.
// Verdicts follow the closed AppSec taxonomy with fixed rules:
//
//	Status 403/401 in prompt  -> WAF_BLOCK  0.90 (demote path)
//	"login" in prompt         -> ADMIN_PANEL 0.85 (promote path)
//	otherwise                 -> UNIQUE_APP  0.85 (promote path)
//
// Every request is counted (see Requests) and logged to the test log.
package mockollama

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Stream   bool      `json:"stream"`
}

// Server wraps an httptest.Server speaking Ollama's /api/chat dialect.
type Server struct {
	t        *testing.T
	srv      *httptest.Server
	requests atomic.Int64
}

// New starts the mock. Close it with s.Close (or t.Cleanup).
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat", s.handleChat)
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"models":[{"name":"mock-llm"}]}`)
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// URL is the base URL to hand to llm.NewClient.
func (s *Server) URL() string { return s.srv.URL }

// Requests returns how many /api/chat calls were served.
func (s *Server) Requests() int64 { return s.requests.Load() }

// Close shuts the server down (subsequent calls fail = degradation path).
func (s *Server) Close() { s.srv.Close() }

func verdictFor(prompt string) (class string, conf float64, reason string) {
	for _, line := range strings.Split(prompt, "\n") {
		trimmed := strings.TrimSpace(line)
		if code, ok := strings.CutPrefix(trimmed, "Status:"); ok {
			code = strings.TrimSpace(code)
			if strings.HasPrefix(code, "403") || strings.HasPrefix(code, "401") {
				return "WAF_BLOCK", 0.9, "mock waf: error status"
			}
		}
	}
	if strings.Contains(strings.ToLower(prompt), "login") {
		return "ADMIN_PANEL", 0.85, "mock app: login surface"
	}
	return "UNIQUE_APP", 0.85, "mock app: custom surface"
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	var req chatRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	var prompt string
	for _, m := range req.Messages {
		if m.Role == "user" {
			prompt = m.Content
		}
	}
	class, conf, reason := verdictFor(prompt)
	n := s.requests.Add(1)
	s.t.Logf("mock-ollama #%d -> %s %.2f", n, class, conf)
	verdict, _ := json.Marshal(map[string]any{
		"classification": class, "confidence": conf, "reason": reason,
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"model":   req.Model,
		"message": map[string]string{"role": "assistant", "content": string(verdict)},
		"done":    true,
	})
}
