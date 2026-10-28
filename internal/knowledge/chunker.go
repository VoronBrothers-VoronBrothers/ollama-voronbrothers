// Package knowledge implements a lightweight RAG index for local text files:
// chunking of .txt/.md documents into ~500-word pieces and BM25 full-text
// search over them via SQLite FTS5. It backs the agent knowledge_search tool
// and the TUI "Knowledge base / RAG" menu item.
package knowledge

import (
	"strings"
)

// Chunking parameters. Chunks are capped at ~ChunkSizeWords so a top-k
// result set stays small enough to fit model context comfortably.
const (
	// ChunkSizeWords is the target maximum size of one chunk in words.
	// ChunkOverlapWords is how many trailing words of an emitted piece are
	// repeated at the start of the next one, so sentences that straddle a
	// hard boundary remain searchable on both sides (~10% of target).
	ChunkOverlapWords = 50
)

// Chunk is one searchable piece of a knowledge base file.
type Chunk struct {
	Path    string `json:"path"`
	Offset  int    `json:"offset"` // byte offset in the file, for citation
	Content string `json:"content"`
}

func wordCount(s string) int { return len(strings.Fields(s)) }

func isSpaceByte(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// chunkText splits text into chunks of at most target words. Structure is
// respected: blank lines break paragraphs, markdown heading lines start new
// sections, and only blocks longer than the target are hard-split on word
// boundaries (with ChunkOverlapWords of overlap).
func chunkText(text string, target int) []Chunk {
	if target <= 0 {
		target = ChunkSizeWords
	}

	var out []Chunk
	var pending []string // piece texts for the current chunk
	pendingStart := 0    // byte offset of the first piece in the current chunk
	pendingWords := 0

	flush := func() {
		if len(pending) == 0 {
			return
		}
		out = append(out, Chunk{Offset: pendingStart, Content: strings.Join(pending, "\n\n")})
		pending = nil
		pendingWords = 0
	}

	for _, blk := range splitBlocks(text) {
		for _, p := range cutLargeBlock(blk.text, target) {
			w := wordCount(p.text)
			if len(pending) > 0 && pendingWords+w > target {
				flush()
			}
			if len(pending) == 0 {
				pendingStart = blk.start + p.start
			}
			pending = append(pending, strings.TrimSpace(p.text))
			pendingWords += w
			if pendingWords >= target {
				flush()
			}
		}
	}
	flush()
	return out
}

type blockPiece struct {
	text  string
	start int // byte offset within the block, not the document
}

// splitBlocks splits text on blank lines; a markdown heading line starts its
// own segment (and stays attached to the text that follows it). Each block
// carries the byte offset where it began in the original text.
func splitBlocks(text string) []blockPiece {
	var blocks []blockPiece
	flushSeg := func(start int, seg []string) {
		if len(seg) == 0 {
			return
		}
		blocks = append(blocks, blockPiece{text: strings.Join(seg, "\n"), start: start})
	}

	var cur []string
	curStart := 0

	for off := 0; off < len(text); {
		rest := text[off:]
		eol := strings.IndexByte(rest, '\n')
		var line string
		if eol >= 0 {
			line = rest[:eol]
		} else {
			line = rest
		}
		nextOff := len(text)
		if eol >= 0 {
			nextOff = off + eol + 1
		}

		if strings.TrimRight(line, " \t\r") == "" {
			flushSeg(curStart, cur)
			cur = nil
			off = nextOff
			continue
		}

		if len(cur) > 0 && isHeading(line) {
			flushSeg(curStart, cur)
			cur = nil
		}
		if len(cur) == 0 {
			curStart = off
		}
		cur = append(cur, line)
		off = nextOff
	}
	flushSeg(curStart, cur)

	return blocks
}

// cutLargeBlock hard-splits a block longer than target words into pieces of
// at most target words, overlapping consecutive pieces by ChunkOverlapWords.
// Blocks that fit are returned as a single piece starting at offset 0.
func cutLargeBlock(block string, target int) []blockPiece {
	if wordCount(block) <= target {
		return []blockPiece{{text: block, start: 0}}
	}

	var starts, ends []int // byte offsets of each word inside the block
	for i := 0; i < len(block); {
		for i < len(block) && isSpaceByte(block[i]) {
			i++
		}
		if i >= len(block) {
			break
		}
		starts = append(starts, i)
		for i < len(block) && !isSpaceByte(block[i]) {
			i++
		}
		ends = append(ends, i)
	}

	// Overlap scales with the target size (10%), capped so a piece always
	// makes progress.
	ov := target * 10 / 100
	if ov <= 0 || target-2*ov <= 0 {
		ov = 0
	}
	step := target - ov
	if step <= 0 {
		step = (target + 1) / 2
	}

	var pieces []blockPiece
	for j := 0; j < len(starts); {
		end := j + step // exclusive word index of this slice
		if end > len(starts) {
			end = len(starts)
		}
		pieces = append(pieces, blockPiece{text: block[starts[j]:ends[end-1]], start: starts[j]})
		if end == len(starts) {
			break
		}
		j += step - ov
	}
	return pieces
}

// isHeading reports whether the line is a markdown heading (1-6 hashes
// followed by whitespace or end-of-line), allowing up to 3 leading spaces.
func isHeading(line string) bool {
	nsp := 0
	for nsp < len(line) && line[nsp] == ' ' {
		nsp++
	}
	if nsp > 3 { // commonmark: max 3 leading spaces before a heading
		return false
	}
	s := line[nsp:]
	n := 0
	for n < len(s) && s[n] == '#' {
		n++
	}
	if n < 1 || n > 6 {
		return false
	}
	return n == len(s) || s[n] == ' ' || s[n] == '\t'
}
