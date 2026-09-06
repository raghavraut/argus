package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raghavraut/rarefy/internal/core"
	"github.com/raghavraut/rarefy/internal/state"
)

func TestComputeMetrics(t *testing.T) {
	truth := map[string]bool{"a": true, "b": true, "c": false, "d": false}
	pred := map[string]bool{"a": true, "b": false, "c": true, "d": false, "extra": true}
	m := computeMetrics(truth, pred)
	if m.tp != 1 || m.fn != 1 || m.fp != 1 || m.tn != 1 {
		t.Fatalf("got %+v", m)
	}
	if m.precision() != 0.5 || m.recall() != 0.5 || m.f1() != 0.5 {
		t.Fatalf("scores p=%.2f r=%.2f f1=%.2f", m.precision(), m.recall(), m.f1())
	}
	// Empty predictions: defined zeros, not NaN.
	empty := computeMetrics(truth, map[string]bool{})
	if empty.precision() != 0 || empty.recall() != 0 || empty.f1() != 0 {
		t.Fatalf("empty scores %+v", empty)
	}
}

func TestEvalEndToEnd(t *testing.T) {
	db := filepath.Join(t.TempDir(), "e.db")
	s, err := state.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	rows := []state.ResultRow{
		{Result: core.TriageResult{Asset: "hot.t", FinalScore: 0.8, Final: true}},
		{Result: core.TriageResult{Asset: "warm.t", FinalScore: 0.4, Final: true}},
		{Result: core.TriageResult{Asset: "cold.t", FinalScore: 0.05, Final: true}},
	}
	if err := s.SaveResults(t.Context(), "c1", rows); err != nil {
		t.Fatal(err)
	}
	// Naive baseline fires only on warm.t.
	if err := s.SaveFindings(t.Context(), "c1", []state.StoredFinding{
		{Asset: "warm.t", TemplateID: "x", Severity: "high", Matched: "m"},
	}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	labels := `{"asset":"hot.t","is_interesting":true}` + "\n" +
		`{"asset":"warm.t","is_interesting":false}` + "\n" +
		`{"asset":"cold.t","is_interesting":false}` + "\n"
	lp := filepath.Join(t.TempDir(), "labels.jsonl")
	if err := os.WriteFile(lp, []byte(labels), 0600); err != nil {
		t.Fatal(err)
	}

	root := NewRoot()
	var b bytes.Buffer
	root.SetOut(&b)
	root.SetErr(&b)
	root.SetArgs([]string{"eval", "--db", db, "--campaign", "c1", "--labels", lp, "--min-score", "0.3"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	// Triage (0.3): hot TP; warm FP (0.4 clears the bar but is boring);
	// cold TN → prec=0.5 rec=1.0 f1=0.667.
	// Naive: warm flagged (FP), hot missed (FN), cold TN → all zeros.
	for _, want := range []string{
		"triage        1    1    1    0  0.500  1.000  0.667",
		"naive         0    1    1    1  0.000  0.000  0.000",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing row %q:\n%s", want, out)
		}
	}
}
