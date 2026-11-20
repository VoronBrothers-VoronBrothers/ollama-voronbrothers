package knowledge

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRecencySortKey checks the pure ranking function: a newer mtime must lower
// (improve) the key, decay toward raw BM25 as age grows, and an unknown date is
// neutral (exactly raw BM25).
func TestRecencySortKey(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	bm25 := -5.0

	fresh   := recencySortKey(bm25, now.Add(-time.Hour), now)
	halfLife := recencySortKey(bm25, now.Add(-RecencyHalfLife), now) // age == half life → boost/2
	ancient := recencySortKey(bm25, now.Add(-10*365*24*time.Hour), now)
	unknown := recencySortKey(bm25, time.Time{}, now)

	const eps = 1e-9
	if unknown != bm25 {
		t.Errorf("no-date mtime must be neutral: got %v want %v", unknown, bm25)
	}
	if !(fresh < halfLife && halfLife < ancient) {
		t.Errorf("want fresh < halfLife < ancient by key, got fresh=%v halfLife=%v ancient=%v", fresh, halfLife, ancient)
	}
	// Boost only ever lowers the key (makes a match rank better), never raises it.
	if ancient > bm25+eps {
		t.Errorf("ancient file must not rank worse than raw BM25: got %v want <= %v", ancient, bm25)
	}
	// A brand-new file gets roughly the full boost applied (well below raw BM25).
	if fresh >= bm25-1.0 {
		t.Errorf("fresh file should get a meaningful recency boost: got %v want < %v", fresh, bm25-1.0)
	}
}

// TestSearchRecencyPrefersNewerFile verifies end-to-end that two files with
// identical (equally relevant) content rank the more recently updated one first.
func TestSearchRecencyPrefersNewerFile(t *testing.T) {
	requireFTS5(t)
	dir := t.TempDir()

	const body = "# Weather\n\nThe weather in Voronezh is rainy and cold today.\n"
	oldPath := filepath.Join(dir, "old.md")
	newPath := filepath.Join(dir, "new.md")
	writeFile(t, oldPath, body)
	writeFile(t, newPath, body)

	now := time.Now()
	_ = os.Chtimes(oldPath, now.Add(-400*24*time.Hour), now.Add(-400*24*time.Hour)) // ~13 months ago
	_ = os.Chtimes(newPath, now.Add(-5*24*time.Hour), now.Add(-5*24*time.Hour))    // 5 days ago

	if _, err := Index(dir); err != nil {
		t.Fatalf("Index: %v", err)
	}

	res, err := Search(dir, "weather voronezh rainy", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) == 0 {
		t.Fatal("want a result")
	}
	if res[0].Path != "new.md" {
		t.Fatalf("want newer file ranked first, got %+v", res)
	}
	// The winner must carry its real (non-zero) modification time for display.
	if res[0].MTime.IsZero() {
		t.Errorf("expected a non-zero mtime on the top result, got %+v", res[0])
	}
}
