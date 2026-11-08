package knowledge

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// requireFTS5 skips the test if the linked SQLite was built without FTS5
// support. This happens when tests are run without `-tags fts5`, which is
// required by mattn/go-sqlite3 to enable the module in its bundled build.
func requireFTS5(t *testing.T) {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Skipf("cannot open sqlite: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE VIRTUAL TABLE t USING fts5(x)`)
	if err != nil {
		t.Skipf("FTS5 not available in this SQLite build (run with -tags fts5): %v", err)
	}
}
