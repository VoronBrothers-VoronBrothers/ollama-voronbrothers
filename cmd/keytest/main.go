package main

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type model struct {
	last []string
}

func (m model) log(line string) {
	f, _ := os.OpenFile("/tmp/keytest.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	defer f.Close()
	fmt.Fprintln(f, line)
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		var hex string
		for _, r := range k.Runes {
			hex += fmt.Sprintf("%02x ", rune(r))
		}
		line := fmt.Sprintf("type=%d str=%q runes=[%s]", k.Type, k.String(), hex)
		m.last = append(m.last, line)
		if len(m.last) > 12 {
			m.last = m.last[len(m.last)-12:]
		}
		m.log(line)
	}
	return m, nil
}

func (m model) View() string {
	return strings.Join(m.last, "\n") + "\n"
}

func main() {
	if err := os.Truncate("/tmp/keytest.log", 0); err != nil {
		fmt.Println("err:", err)
		return
	}
	p := tea.NewProgram(model{})
	if _, err := p.Run(); err != nil {
		fmt.Println("err:", err)
	}
}
