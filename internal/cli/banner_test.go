package cli

import (
	"strings"
	"testing"
)

// The wordmark's spacing is significant (26-space row-1 indent,
// 36-column grid, meaningful trailing spaces). Editors love stripping
// trailing whitespace — this test fails loudly if that happens.
func TestBannerExact(t *testing.T) {
	rows := strings.Split(banner, "\n")
	if len(rows) != 5 {
		t.Fatalf("banner has %d rows, want 5", len(rows))
	}
	for i, r := range rows {
		if len(r) != 36 {
			t.Fatalf("row %d width %d, want 36: %q", i, len(r), r)
		}
	}
	if !strings.HasPrefix(rows[0], strings.Repeat(" ", 26)+"_|") {
		t.Fatalf("row 1 missing 26-space indent: %q", rows[0])
	}
	if !strings.HasSuffix(rows[4], "____/  ") {
		t.Fatalf("row 5 tail changed: %q", rows[4])
	}
}
