package knowledge

import (
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	_ "github.com/mattn/go-sqlite3"
)

// IndexFileName is the index database stored next to the knowledge files.
var schema = `
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT
);
CREATE TABLE IF NOT EXISTS chunks (
	id      INTEGER PRIMARY KEY,
	path    TEXT NOT NULL,
	offset  INTEGER NOT NULL,
	content TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_chunks_path ON chunks(path);
CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
	content,
	content='chunks', content_rowid='id',
	tokenize='porter unicode61'
);
`

// supportedExtensions lists the file types the indexer knows about. Adding a
// new format = adding an extension here plus (if not plain text) a loader in
// loadFile.
var supportedExtensions = []string{".txt", ".md"}

// Store is a handle on one knowledge index database.
type Store struct {
	conn *sql.DB
}

// Result is one search hit with its source file and byte offset.
type Result struct {
	Chunk
	Score float64 // BM25 score, lower (more negative) = better match
}

// Status summarises the index for UI display (TUI menu).
type Status struct {
	Files     int
	Chunks    int
	IndexedAt time.Time
	Stale     bool
}

func openStore(path string) (*Store, error) {
	conn, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open knowledge index: %w", err)
	}
	if _, err := conn.Exec(schema); err != nil {
		conn.Close()
		return nil, fmt.Errorf("init knowledge schema: %w", err)
	}
	s := &Store{conn: conn}
	if err := s.conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ping knowledge index: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.conn.Close() }

// collectFiles walks dir and returns the knowledge files in it: regular
// files with a supported extension. Hidden files/dirs and the index itself
// are skipped.
func collectFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(name))
		if !slices.Contains(supportedExtensions, ext) {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk knowledge dir: %w", err)
	}
	slices.Sort(files)
	return files, nil
}

// loadFile reads a knowledge file and returns its text. The switch is the
// loader registry for future formats (pdf/docx ...).
func loadFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(b), nil
}

// Index rebuilds the full index for all supported files under dir. It is
// idempotent and safe to call whenever files change (lazy reindex on search).
func Index(dir string) (*Status, error) {
	files, err := collectFiles(dir)
	if err != nil {
		return nil, err
	}

	s, err := openStore(filepath.Join(dir, IndexFileName))
	if err != nil {
		return nil, err
	}
	defer s.Close()

	tx, err := s.conn.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin index txn: %w", err)
	}
	rollback := func(err error) (*Status, error) {
		_ = tx.Rollback()
		return nil, err
	}

	if _, err := tx.Exec("DELETE FROM chunks"); err != nil {
		return rollback(fmt.Errorf("clear chunks: %w", err))
	}
	insertChunk, err := tx.Prepare("INSERT INTO chunks (path, offset, content) VALUES (?, ?, ?)")
	if err != nil {
		return rollback(err)
	}
	defer insertChunk.Close()

	chunksTotal := 0
	for _, f := range files {
		text, err := loadFile(f)
		if err != nil {
			continue // unreadable file: skip, keep indexing the rest
		}
		rel, _ := filepath.Rel(dir, f)
		for _, c := range chunkText(text, ChunkSizeWords) {
			if _, err := insertChunk.Exec(rel, c.Offset, c.Content); err != nil {
				return rollback(fmt.Errorf("insert chunk for %s: %w", rel, err))
			}
			chunksTotal++
		}
	}

	// Refresh the FTS index (external-content table) from chunks.
	if _, err := tx.Exec("INSERT INTO chunks_fts(chunks_fts) VALUES ('rebuild')"); err != nil {
		return rollback(fmt.Errorf("refresh fts: %w", err))
	}
	if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES ('indexed_at', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, time.Now().Format(time.RFC3339)); err != nil {
		return rollback(fmt.Errorf("store indexed_at: %w", err))
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit index txn: %w", err)
	}

	return &Status{Files: len(files), Chunks: chunksTotal}, nil
}

// Search runs a BM25 full-text search over the index and returns up to k
// best matching chunks with their source files.
func Search(dir string, query string, k int) ([]Result, error) {
	if k <= 0 {
		k = 3
	}
	tokens := ftsTokens(query)
	if len(tokens) == 0 {
		return nil, nil
	}

	s, err := openStore(filepath.Join(dir, IndexFileName))
	if err != nil {
		return nil, err
	}
	defer s.Close()

	match := strings.Join(tokens, " OR ")
	rows, err := s.conn.Query(
		`SELECT c.path, c.offset, c.content, bm25(chunks_fts) AS score
		 FROM chunks_fts JOIN chunks c ON c.id = chunks_fts.rowid
		 WHERE chunks_fts MATCH ?
		 ORDER BY score LIMIT ?`, match, k)
	if err != nil {
		return nil, fmt.Errorf("fts query: %w", err)
	}
	defer rows.Close()

	var out []Result
	for rows.Next() {
		var r Result
		if err := rows.Scan(&r.Path, &r.Offset, &r.Content, &r.Score); err != nil {
			return nil, fmt.Errorf("scan fts row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ftsTokens turns a free-text query into FTS5 match syntax: one double-quoted
// term per word, implicitly ANDed. Returns nil when nothing is searchable.
func ftsTokens(query string) []string {
	var tokens []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, `"`+cur.String()+`"`)
			cur.Reset()
		}
	}
	for _, r := range query {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return tokens
}

// Status reports file/chunk counts and whether the index lags behind the
// files on disk. A missing directory or database counts as empty, not stale.
func IndexStatus(dir string) (*Status, error) {
	st := &Status{}

	files, err := collectFiles(dir)
	if err != nil {
		return nil, err
	}
	st.Files = len(files)

	dbPath := filepath.Join(dir, IndexFileName)
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			st.Stale = len(files) > 0
			return st, nil
		}
		return nil, fmt.Errorf("stat index: %w", err)
	}

	s, err := openStore(dbPath)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	var n int
	if err := s.conn.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&n); err == nil {
		st.Chunks = n
	}
	var ts string
	if err := s.conn.QueryRow(`SELECT value FROM meta WHERE key='indexed_at'`).Scan(&ts); err == nil {
		st.IndexedAt, _ = time.Parse(time.RFC3339, ts)
	}

	for _, f := range files {
		var count int
		err = s.conn.QueryRow(`SELECT COUNT(*) FROM chunks WHERE path = ?`, relPath(dir, f)).Scan(&count)
		if err != nil {
			return st, fmt.Errorf("check staleness: %w", err)
		}
		text, _ := loadFile(f)
		want := len(chunkText(text, ChunkSizeWords))
		if count != want {
			st.Stale = true
		}
	}
	return st, nil
}

func relPath(dir, f string) string {
	r, err := filepath.Rel(dir, f)
	if err != nil {
		return f
	}
	return r
}
