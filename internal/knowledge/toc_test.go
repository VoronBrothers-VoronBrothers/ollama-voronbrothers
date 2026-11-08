package knowledge

import (
	"strings"
	"testing"
)

func TestStripTOCBasic(t *testing.T) {
	text := `Оглавление
1. Введение ................................................... 3
2. Основные понятия .......................................... 5
3. Классификация .............................................. 8
4. Диагностика ............................................... 12
5. Лечение .................................................... 15

Введение
Это основной текст документа, который должен сохраниться после удаления оглавления.
`
	result := stripTOC(text)

	// TOC lines should be removed
	if strings.Contains(result, "Введение ...") || strings.Contains(result, "................... 3") {
		t.Errorf("TOC entries still present in result:\n%s", result)
	}

	// Real content must survive
	if !strings.Contains(result, "основной текст документа") {
		t.Error("real content was stripped along with TOC")
	}
}

func TestStripTOCNoTOC(t *testing.T) {
	text := `Введение
Это просто текст без оглавления. Ничего не должно измениться.
`
	result := stripTOC(text)
	if result != text {
		t.Errorf("text without TOC was modified:\n%s", result)
	}
}

func TestStripTOCShortDoc(t *testing.T) {
	// Fewer than 5 TOC lines → not detected as TOC
	text := `1. Item one .............. 2
2. Item two ............... 3
Normal content here that should stay.
`
	result := stripTOC(text)
	if !strings.Contains(result, "Item one") {
		t.Error("short list of 2 entries was incorrectly stripped as TOC")
	}
}

func TestStripTOCTocInMiddle(t *testing.T) {
	text := `Intro paragraph that should be kept.

Оглавление
1. Section A ................................... 10
2. Section B .................................. 14
3. Section C .................................. 18
4. Section D .................................. 22
5. Section E .................................. 27

Real content after TOC that must survive.
`
	result := stripTOC(text)

	if !strings.Contains(result, "Intro paragraph") {
		t.Error("intro text before TOC was lost")
	}
	if strings.Contains(result, "Section A .......") {
		t.Errorf("TOC entries not stripped:\n%s", result)
	}
	if !strings.Contains(result, "Real content after TOC") {
		t.Error("content after TOC was lost")
	}
}
