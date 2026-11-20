// Package knowledge implements a simple RAG (Retrieval-Augmented Generation)
// catalog for loading external documents and searching them with BM25.
//
// # Architecture (current: stub / MVP skeleton)
//
//	~/.ollama/knowledge/          — document storage (user puts files here)
//	~/.ollama/knowledge/.index.db — SQLite FTS5 index (BM25 ranking)
//
// Documents are chunked (~500 words per chunk) and indexed on add.
// Search returns top-N relevant chunks to inject into model context.
//
// # Future: embedding-based semantic search
//
// When BM25 stops giving relevant results (>100 docs), plug in a local
// multilingual embedding model (e.g. bge-m3, nomic-embed-text) via
// /api/embed. Vectors go into SQLite via sqlite-vec extension. Search
// becomes hybrid: BM25 + cosine similarity, reranked.
//
// # Why models answer "dryly" after RAG search
//
// Known effect: after receiving retrieved chunks, the model switches to
// "answer from source" mode — tone becomes flat, citation-heavy. Mitigations:
//  1. Limit results: ≤3 chunks, each ≤500 words (don't flood context).
//  2. Tool description explicitly says "keep natural tone, don't quote verbatim".
//  3. Don't inject raw chunk text — summarize/extract key facts only.
package knowledge

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// DirName is the subdirectory under ~/.ollama for knowledge docs.
	DirName = "knowledge"

	// IndexFileName is the SQLite FTS5 database file name.
	IndexFileName = ".index.db"

	// MaxChunks is the max number of chunks returned per search query.
	// Keeping this low prevents context flooding and "dry answers".
	MaxChunks = 3

	// ChunkSizeWords is approximate words per chunk during document splitting.
	// 250 keeps chunks small enough that a single heading section (e.g. a
	// definition paragraph) fits in one chunk without being diluted by a TOC
	// or adjacent content. Overlap scales at ~10% (see ChunkOverlapWords).
	ChunkSizeWords = 250
)

const (
	// RecencyBoost is how many BM25-score units a brand-new file's effective
	// score is pushed down (better) by recency, before decay. Higher means
	// dates matter more relative to raw relevance. Kept modest so an unrelated
	// fresh doc doesn't leapfrog the genuinely best match.
	RecencyBoost = 3.0

	// RecencyHalfLife is the age at which a file's recency boost decays to half.
	// ~6 months: recent notes get a real lift, old ones fall back to pure BM25.
	RecencyHalfLife = 180 * 24 * time.Hour
)

// Dir returns the absolute path to the knowledge directory, creating it if needed.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("knowledge: cannot determine home dir: %w", err)
	}
	dir := filepath.Join(home, ".ollama", DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("knowledge: create dir: %w", err)
	}
	return dir, nil
}

// IndexPath returns the path to the SQLite FTS5 index file.
func IndexPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, IndexFileName), nil
}
