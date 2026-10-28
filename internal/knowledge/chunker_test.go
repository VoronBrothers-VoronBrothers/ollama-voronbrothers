package knowledge

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestChunkTextSingleSmallDoc(t *testing.T) {
	text := "# Title\n\nFirst paragraph.\n\nSecond paragraph."
	chunks := chunkText(text, 500)
	if len(chunks) != 1 {
		t.Fatalf("want 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Offset != 0 {
		t.Errorf("want offset 0, got %d", chunks[0].Offset)
	}
	for _, para := range []string{"# Title", "First paragraph.", "Second paragraph."} {
		if !strings.Contains(chunks[0].Content, para) {
			t.Errorf("chunk lost part of the document: %q", chunks[0].Content)
		}
	}
}

func TestChunkTextSplitsByTarget(t *testing.T) {
	// 3 distinct sections, each ~25 words => at target 40 they must split.
	mk := func(prefix string) string { return prefix + " " + strings.Repeat("word ", 25) }
	text := mk("alpha") + "\n\n" + mk("beta") + "\n\n" + "# Head\n\n" + mk("gamma")
	chunks := chunkText(text, 40)

	if len(chunks) < 3 {
		t.Fatalf("want at least 3 chunks, got %d", len(chunks))
	}
	for _, c := range chunks {
		if w := wordCount(c.Content); w > 52 { // target + overlap slack
			t.Errorf("chunk too large: %d words", w)
		}
	}
	// Every section marker must be findable in some chunk.
	all := ""
	for _, c := range chunks {
		all += " " + c.Content
	}
	for _, want := range []string{"alpha", "beta", "# Head", "gamma"} {
		if !strings.Contains(all, want) {
			t.Errorf("lost content %q while chunking", want)
		}
	}
}

func TestChunkTextOffsetsPointAtContent(t *testing.T) {
	text := strings.Repeat("short paragraph one. ", 3) + "\n\n" + "unique_marker_word here\n\n" + strings.Repeat("tail text two. ", 3)
	chunks := chunkText(text, 500)
	found := false
	for _, c := range chunks {
		if !strings.Contains(c.Content, "unique_marker_word") {
			continue
		}
		firstWord := strings.Fields(c.Content)[0]
		if c.Offset >= len(text) || !strings.HasPrefix(text[c.Offset:], firstWord) {
			t.Errorf("offset %d does not point at chunk content (want word %q)", c.Offset, firstWord)
		}
		found = true
	}
	if !found {
		t.Fatal("chunk with marker not found")
	}
}

func TestChunkTextHardSplitWithOverlap(t *testing.T) {
	text := strings.Join(makeWords(200), " ")
	pieces := cutLargeBlock(text, 50)
	if len(pieces) < 3 {
		t.Fatalf("want >= 3 hard-split pieces, got %d", len(pieces))
	}
	totalWords := wordCount(text)
	// Overlap means total words across pieces > total document words.
	sum := 0
	for _, p := range pieces {
		sum += wordCount(p.text)
	}
	if sum <= totalWords {
		t.Errorf("no overlap between hard-split pieces (sum=%d, doc=%d)", sum, totalWords)
	}
	// Pieces must start at real byte offsets of their first word.
	for _, p := range pieces {
		if !strings.HasPrefix(text[p.start:], strings.Fields(p.text)[0]) {
			t.Errorf("piece offset %d does not align with its first word", p.start)
		}
	}
}

func TestChunkTextEmpty(t *testing.T) {
	if chunks := chunkText("", 500); len(chunks) != 0 {
		t.Fatalf("want no chunks for empty text, got %d", len(chunks))
	}
	if chunks := chunkText("   \n\n  ", 500); len(chunks) != 0 {
		t.Fatalf("want no chunks for whitespace-only text, got %d", len(chunks))
	}
}

func TestIsHeading(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"# H1", true}, {"###### six", true}, {"####### seven", false},
		{"#nospace", false}, {"  # two leading spaces", true},
		{"    # four leading spaces", false}, {"text # not heading", false},
		{"#", true},
	}
	for _, c := range cases {
		if got := isHeading(c.line); got != c.want {
			t.Errorf("isHeading(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestIndexAndSearchRoundtrip(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "alpha.md"), "# Ollama notes\n\nOllama is a local model runner with an HTTP API.\n")
	writeFile(t, filepath.Join(dir, "beta.txt"), "The weather in Voronezh today is rainy and cold.\n")
	// unsupported + hidden files must be ignored
	writeFile(t, filepath.Join(dir, "skip.pdf"), "%PDF fake\n")
	writeFile(t, filepath.Join(dir, ".hidden.md"), "secret content\n")

	st, err := Index(dir)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if st.Files != 2 || st.Chunks < 2 {
		t.Fatalf("want 2 files indexed with chunks, got %+v", st)
	}

	res, err := Search(dir, "local model runner API", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) == 0 || res[0].Path != "alpha.md" {
		t.Fatalf("want alpha.md as top hit, got %+v", res)
	}

	res, _ = Search(dir, "weather voronezh", 3)
	if len(res) == 0 || res[0].Path != "beta.txt" {
		t.Fatalf("want beta.txt as top hit, got %+v", res)
	}

	// No match: zero rows, no error.
	res, err = Search(dir, "zzzqqq xxx", 3)
	if err != nil || len(res) != 0 {
		t.Fatalf("want no results for gibberish, got %+v (err=%v)", res, err)
	}

	st, _ = IndexStatus(dir)
	if st.Stale {
		t.Errorf("fresh index reported stale")
	}
}

func TestStatusDetectsStaleness(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.md"), "# one\n\ntext here\n")
	if _, err := Index(dir); err != nil {
		t.Fatal(err)
	}

	// New file appears on disk.
	writeFile(t, filepath.Join(dir, "b.txt"), "brand new content\n")
	st, err := IndexStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Stale {
		t.Error("index with a new unindexed file must be stale")
	}

	// Rebuild clears staleness.
	if _, err := Index(dir); err != nil {
		t.Fatal(err)
	}
	st, _ = IndexStatus(dir)
	if st.Stale {
		t.Error("reindexed index still reported stale")
	}
}

func TestIndexMissingDir(t *testing.T) {
	dir := t.TempDir() + "/nope"
	res, err := Search(dir, "anything", 3)
	if err == nil && len(res) != 0 {
		t.Fatalf("want no results for missing dir, got %+v", res)
	}
	st, err := IndexStatus(dir)
	if err != nil {
		t.Fatalf("Status on missing dir: %v", err)
	}
	if st.Stale || st.Files != 0 {
		t.Errorf("missing dir must be empty and not stale, got %+v", st)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func makeWords(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "w" + string(rune('a'+i%26)) + strconv.Itoa(i)
	}
	return out
}

func TestChunkHeadingPropagation(t *testing.T) {
	text := strings.Repeat("word ", 10) + "\n\n# Section A\n" + strings.Repeat("alpha ", 20) + "\n\n" + strings.Repeat("beta ", 20)
	chunks := chunkText(text, 5)
	if len(chunks) == 0 {
		t.Fatal("no chunks")
	}
	sawHeading := false
	for _, c := range chunks {
		if strings.Contains(c.Content, "alpha") && !strings.HasPrefix(c.Heading, "# Section A") {
			t.Errorf("chunk with 'alpha' has heading %q, want '# Section A'", c.Heading)
		}
		if c.Heading != "" {
			sawHeading = true
		}
	}
	if !sawHeading {
		t.Error("no chunk carried a heading")
	}
}
