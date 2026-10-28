package chat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type attachedFile struct {
	Path    string
	Content string
	Lines   int
}

var languageByExt = map[string]string{
	".go":     "go",
	".py":     "python",
	".js":     "javascript",
	".ts":     "typescript",
	".jsx":    "jsx",
	".tsx":    "tsx",
	".rs":     "rust",
	".c":      "c",
	".cpp":    "cpp",
	".h":      "c",
	".java":   "java",
	".sh":     "bash",
	".bash":   "bash",
	".sql":    "sql",
	".json":   "json",
	".jsonl":  "jsonl",
	".yaml":   "yaml",
	".yml":    "yaml",
	".toml":   "toml",
	".md":     "markdown",
	".markdown": "markdown",
	".txt":    "text",
	".csv":    "csv",
	".env":    "text",
}

var binaryExts = map[string]bool{
	".exe": true, ".bin": true, ".pyc": true, ".class": true,
	".pdf": true, ".doc": true, ".docx": true, ".png": true,
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
		pluralSuffix(lines),
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
	lines := bytes.Split(content, []byte{'\n'})
	previewLines := 12
	if len(lines) < previewLines {
		previewLines = len(lines)
	}

	var preview []string
	for i := 0; i < previewLines; i++ {
		if trimmed := strings.TrimSpace(string(lines[i])); trimmed != "" {
			preview = append(preview, trimmed)
		}
	}

	previewText := strings.Join(preview, "\n")
	if totalLines > previewLines {
		previewText += fmt.Sprintf("\n... (и ещё %d записей)", totalLines-previewLines)
	}

	header := fmt.Sprintf("--- %s (JSON Lines, %d записей) ---", filepath.Base(path), totalLines)
	return header + "\n```jsonl\n" + previewText + "\n```\n"
}

func pluralSuffix(n int) string {
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
