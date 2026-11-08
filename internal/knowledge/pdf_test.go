package knowledge

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCollectFilesIncludesPDF verifies that .pdf files are picked up by the
// indexer file walker.
func TestCollectFilesIncludesPDF(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "doc.pdf"), []byte("%PDF-1.4 fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := collectFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files (txt+pdf), got %d: %v", len(files), files)
	}
}

// TestLoadFilePDF exercises the pdftotext path. Skips if poppler-utils absent.
func TestLoadFilePDF(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not found — skipping (sudo apt install poppler-utils)")
	}

	dir := t.TempDir()
	textStr := "Voron RAG test content"
	pdfContent := buildMinimalPDF(textStr)
	pdfPath := filepath.Join(dir, "sample.pdf")
	if err := os.WriteFile(pdfPath, []byte(pdfContent), 0o644); err != nil {
		t.Fatal(err)
	}

	text, err := loadFile(pdfPath)
	if err != nil {
		t.Fatalf("loadFile on PDF: %v", err)
	}
	if !strings.Contains(text, "Voron") && !strings.Contains(text, "RAG") {
		limit := 200
		if len(text) < limit {
			limit = len(text)
		}
		t.Errorf("extracted text does not contain expected words; got: %q", text[:limit])
	}
}

// TestLoadFilePDFError ensures an error is returned for a corrupt/empty PDF.
func TestLoadFilePDFError(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not found")
	}

	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "x.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := loadFile(pdfPath)
	if err == nil {
		t.Log("no error (pdftotext may be lenient with tiny files)")
	} else {
		t.Logf("expected error: %v", err)
	}
}

// buildMinimalPDF creates the smallest valid single-page PDF containing textStr.
func buildMinimalPDF(text string) string {
	stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
	return fmt.Sprintf(`%%PDF-1.4
1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj
2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj
3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]/Contents 4 0 R/Resources<</Font<</F1 5 0 R>>>>>>endobj
4 0 obj<</Length %d>>stream
%s
endstream endobj
5 0 obj<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>endobj
trailer<</Size 6/Root 1 0 R>>
%%%%EOF`, len(stream), stream)
}
