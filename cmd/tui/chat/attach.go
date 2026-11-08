package chat

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type attachedFile struct {
	Path    string
	Content string
	Lines   int
}

var languageByExt = map[string]string{
	".go":       "go",
	".py":       "python",
	".js":       "javascript",
	".ts":       "typescript",
	".jsx":      "jsx",
	".tsx":      "tsx",
	".rs":       "rust",
	".c":        "c",
	".cpp":      "cpp",
	".h":        "c",
	".java":     "java",
	".sh":       "bash",
	".bash":     "bash",
	".sql":      "sql",
	".json":     "json",
	".jsonl":    "jsonl",
	".yaml":     "yaml",
	".yml":      "yaml",
	".toml":     "toml",
	".md":       "markdown",
	".markdown": "markdown",
	".txt":      "text",
	".csv":      "csv",
	".pdf":      "text",
	".env":      "text",
}

var binaryExts = map[string]bool{
	".exe": true, ".bin": true, ".pyc": true, ".class": true,
	".doc": true, ".docx": true, ".png": true,
	".jpg": true, ".jpeg": true, ".webp": true, ".gif": true,
	".zip": true, ".tar": true, ".gz": true,
}

// formatAttachedFile возвращает красиво отформатированный блок для /add
// Уровень "Хороший": название файла, язык, количество строк, markdown code block.
func formatAttachedFile(path string, content []byte) (string, error) {
	if len(content) == 0 {
		return "", fmt.Errorf("файл пустой")
	}

	ext := strings.ToLower(filepath.Ext(path))
	lang, ok := languageByExt[ext]
	if !ok {
		lang = "text"
	}

	text := strings.TrimSpace(string(content))
	lines := strings.Count(text, "\n") + 1

	// Специальная обработка jsonl
	if ext == ".jsonl" {
		lines = countJSONL(content)
		return formatJSONL(path, content, lines), nil
	}

	header := fmt.Sprintf("--- %s (%s, %d строк%s) ---",
		filepath.Base(path),
		lang,
		lines,
		ruPluralSuffix(lines),
	)

	codeBlock := fmt.Sprintf("```%s\n%s\n```", lang, text)

	return header + "\n" + codeBlock + "\n", nil
}

func countJSONL(data []byte) int {
	count := 0
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) > 0 {
			count++
		}
	}
	return count
}

func formatJSONL(path string, content []byte, totalLines int) string {
	text := strings.TrimSpace(string(content))
	header := fmt.Sprintf("--- %s (JSON Lines, %d записей) ---", filepath.Base(path), totalLines)
	return header + "\n```jsonl\n" + text + "\n```\n"
}

func ruPluralSuffix(n int) string {
	if n%10 == 1 && n%100 != 11 {
		return "а"
	}
	if n%10 >= 2 && n%10 <= 4 && (n%100 < 10 || n%100 >= 20) {
		return "и"
	}
	return ""
}

// isSafeToAttach возвращает true, если файл можно безопасно читать как текст
func isSafeToAttach(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if binaryExts[ext] {
		return false
	}
	return true
}

// extractPDFText достаёт копируемый текст из PDF через pdftotext (poppler-utils).
func extractPDFText(path string) ([]byte, error) {
	out, err := exec.Command("pdftotext", path, "-").Output()
	if err != nil {
		switch {
		case errors.Is(err, exec.ErrNotFound):
			return nil, fmt.Errorf("pdftotext не найден (sudo apt install poppler-utils)")
		case len(out) > 0:
			// pdftotext иногда стонет в stderr, но текст извлёк — берём его
		default:
			return nil, fmt.Errorf("pdftotext: %v", err)
		}
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, fmt.Errorf("в PDF нет текстового слоя (скан — нужен OCR)")
	}
	return out, nil
}

// readAttachData читает содержимое файла для /add:
// PDF через pdftotext, остальное как обычный текст.
func readAttachData(path string) ([]byte, error) {
	if strings.ToLower(filepath.Ext(path)) == ".pdf" {
		return extractPDFText(path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать: %v", err)
	}
	return data, nil
}

// readAndFormatFile читает файл и возвращает отформатированный блок
func readAndFormatFile(path string) (string, error) {
	if !isSafeToAttach(path) {
		return "", fmt.Errorf("файл %s выглядит как бинарный, пропущен", filepath.Base(path))
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("не удалось прочитать %s: %v", filepath.Base(path), err)
	}

	formatted, err := formatAttachedFile(path, data)
	if err != nil {
		return "", err
	}
	return formatted, nil
}
