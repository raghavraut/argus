package state

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMarkDoneResume(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "resume.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if done, _ := s.IsDone(ctx, "c1", "a.t", "probe"); done {
		t.Fatal("fresh db must report not-done")
	}
	s.MarkDone("c1", "a.t", "probe")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if done, _ := s.IsDone(ctx, "c1", "a.t", "probe"); !done {
		t.Fatal("after flush must report done")
	}
	// Pending (unflushed) reads must also report done.
	s.MarkDone("c1", "b.t", "probe")
	if done, _ := s.IsDone(ctx, "c1", "b.t", "probe"); !done {
		t.Fatal("pending write must report done")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopen: durability check.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	if done, _ := s2.IsDone(ctx, "c1", "a.t", "probe"); !done {
		t.Fatal("reopened db must retain completions")
	}
}
