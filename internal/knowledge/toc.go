package knowledge

import (
	"regexp"
	"strings"
)

// tocLinePattern matches a single table-of-contents entry line. The hallmark
// is a run of 3+ dot characters followed by optional whitespace and a page
// number at the end of line. This covers typical pdftotext output like:
//
//	1. Краткая информация ............ 12
//	Термины и определения ............. 8
//
// We require 3 dots to avoid false positives on ellipses in prose (which are
// usually at most 2 dots or surrounded by spaces differently).
var tocLinePattern = regexp.MustCompile(`\.{3,}\s*\d+\s*$`)

// stripTOC removes table-of-contents sections from extracted PDF text before
// chunking. TOC entries pollute BM25 because they repeat key terms many times
// in a small area (inflating term frequency) without adding real content.
//
// Strategy: scan lines; if we find ≥ minTocEntries consecutive "TOC-style"
// lines within any window of maxWindow lines, remove the entire contiguous
// block from the first TOC line to the last. We allow a few non-matching
// lines between entries (blank lines, section headers like "Оглавление").
func stripTOC(text string) string {
	lines := strings.Split(text, "\n")

	const minTocEntries = 5 // minimum matching lines in a window to call it TOC
	const maxWindow = 40   // search within at most this many consecutive lines

	n := len(lines)
	if n < minTocEntries {
		return text
	}

	isTOCLine := func(l string) bool { return tocLinePattern.MatchString(l) }

	// Find the largest contiguous "TOC region": a stretch of lines where at
	// least minTocEntries match the pattern, allowing up to 3 non-matching
	// lines (blanks, sub-headers) between matches.
	var start, end int // inclusive range of lines to remove
	found := false

	for i := 0; i < n && !found; i++ {
		count := 0
		j := i
		gaps := 0
		lastMatch, firstMatch := -1, -1
		for j < n && (j-i) < maxWindow {
			if isTOCLine(lines[j]) {
				if firstMatch == -1 {
					firstMatch = j
				}
				count++
				lastMatch = j
			} else if count > 0 {
				gaps++
				if gaps > 3 {
					break // too many non-TOC lines; region ends
				}
			}
			j++
		}
		if count >= minTocEntries {
			start = firstMatch
			end = lastMatch + 1
			found = true
		}
	}

	if !found {
		return text
	}

	// Remove the TOC region. Keep everything before and after.
	kept := make([]string, 0, n-(end-start))
	kept = append(kept, lines[:start]...)
	kept = append(kept, lines[end:]...)
	return strings.Join(kept, "\n")
}
