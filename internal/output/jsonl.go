// Package output enforces the Unix contract: strict JSONL on stdout,
// human logs on stderr. Triage verdicts and nuclei findings are the only
// line types; findings carry {"type":"nuclei_finding"} so consumers can
// split the stream deterministically.
package output

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"sync"

	"github.com/raghavraut/rarefy/internal/core"
)

// Writer is a goroutine-safe JSONL emitter to stdout.
type Writer struct {
	mu  sync.Mutex
	enc *json.Encoder
}

// NewWriter returns a Writer over w (main passes os.Stdout).
func NewWriter(w io.Writer) *Writer {
	return &Writer{enc: json.NewEncoder(w)}
}

// Emit writes one TriageResult as a single JSON line.
func (w *Writer) Emit(r core.TriageResult) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.enc.Encode(r)
}

// Provisional emits a Phase-1 heuristic result (Final=false).
func (w *Writer) Provisional(asset string, score, conf float64, evidence []string) error {
	return w.Emit(core.TriageResult{
		Asset: asset, FinalScore: score, Confidence: conf,
		Evidence: evidence, Final: false, Recommendation: "provisional",
	})
}

// EmitFinding writes one nuclei finding (any struct with a "type"
// discriminator) as a single JSON line. Goroutine-safe like Emit.
func (w *Writer) EmitFinding(f any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.enc.Encode(f)
}

// Stderr logger for everything else.
func Logger(prefix string) *log.Logger {
	return log.New(os.Stderr, prefix, log.LstdFlags)
}
