package tui

import (
	"fmt"
	"os/exec"
	"runtime"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ollama/ollama/cmd/launch"
	knowledge "github.com/ollama/ollama/internal/knowledge"
	"github.com/ollama/ollama/version"
)

var (
	versionStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "243", Dark: "250"})

	menuItemStyle = lipgloss.NewStyle().
			PaddingLeft(2)

	menuSelectedItemStyle = lipgloss.NewStyle().
				Bold(true).
				Background(lipgloss.AdaptiveColor{Light: "254", Dark: "236"})

	menuDescStyle = selectorDescStyle.
			PaddingLeft(4)

	greyedStyle = menuItemStyle.
			Foreground(lipgloss.AdaptiveColor{Light: "242", Dark: "246"})

	greyedSelectedStyle = menuSelectedItemStyle.
				Foreground(lipgloss.AdaptiveColor{Light: "242", Dark: "246"})

	modelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "243", Dark: "250"})

	notInstalledStyle = lipgloss.NewStyle().
				Foreground(lipgloss.AdaptiveColor{Light: "242", Dark: "246"}).
				Italic(true)
)

type menuItem struct {
	title       string
	description string
	integration string
	isRunModel  bool
	isKnowledge bool
	isOthers    bool
}

var runModelMenuItem = menuItem{
	title:       "Working and interacting with a model",
	description: "If you need a model for testing, use either the CPU-assistant or the orchestrator that is already loaded",
	isRunModel:  true,
}

var knowledgeMenuItem = menuItem{
	title:       "Knowledge base / RAG",
	description: "Search local documents (.txt/.md): index status, reindex, open folder",
	isKnowledge: true,
}

var othersMenuItem = menuItem{
	title:       "More...",
	description: "Show additional integrations",
	isOthers:    true,
}

// launcherMenuIntegrations defines the integrations pinned to the root menu.
// Additional visible integrations are available through More in registry order.
var launcherMenuIntegrations = []string{"claude", "opencode", "hermes", "openclaw"}

type model struct {
	state      *launch.LauncherState
	items      []menuItem
	cursor     int
	showOthers bool
	width      int
	quitting   bool
	selected   bool
	action     TUIAction

	// Knowledge base / RAG sub-screen state.
	showKB bool
	kbText string // multi-line status shown on the KB screen
	kbBusy bool
}

func newModel(state *launch.LauncherState) model {
	m := model{
		state: state,
	}
	m.showOthers = shouldExpandOthers(state)
	m.items = buildMenuItems(state, m.showOthers)
	m.cursor = initialCursor(state, m.items)
	return m
}

func shouldExpandOthers(state *launch.LauncherState) bool {
	if state == nil {
		return false
	}
	for _, item := range otherIntegrationItems(state) {
		if item.integration == state.LastSelection {
			return true
		}
	}
	return false
}

func buildMenuItems(state *launch.LauncherState, showOthers bool) []menuItem {
	return []menuItem{runModelMenuItem, knowledgeMenuItem}
}

func integrationMenuItem(state launch.LauncherIntegrationState) menuItem {
	description := state.Description
	if description == "" {
		description = "Open " + state.DisplayName + " integration"
	}
	return menuItem{
		title:       "Launch " + state.DisplayName,
		description: description,
		integration: state.Name,
	}
}

func launcherIntegrationItems(state *launch.LauncherState) []menuItem {
	if state == nil {
		return nil
	}

	items := make([]menuItem, 0, len(launcherMenuIntegrations))
	for _, name := range launcherMenuIntegrations {
		integrationState, ok := state.Integrations[name]
		if !ok {
			continue
		}
		items = append(items, integrationMenuItem(integrationState))
	}
	return items
}

func otherIntegrationItems(state *launch.LauncherState) []menuItem {
	if state == nil {
		return nil
	}

	pinned := make(map[string]bool, len(launcherMenuIntegrations))
	for _, name := range launcherMenuIntegrations {
		pinned[name] = true
	}

	items := make([]menuItem, 0, len(state.Integrations))
	for _, info := range launch.ListIntegrationInfos() {
		if pinned[info.Name] {
			continue
		}
		integrationState, ok := state.Integrations[info.Name]
		if !ok {
			continue
		}
		items = append(items, integrationMenuItem(integrationState))
	}
	return items
}

func primaryMenuItemCount(state *launch.LauncherState) int {
	return 2 + len(launcherIntegrationItems(state))
}

func initialCursor(state *launch.LauncherState, items []menuItem) int {
	if state == nil || state.LastSelection == "" {
		return 0
	}
	for i, item := range items {
		if state.LastSelection == "run" && item.isRunModel {
			return i
		}
		if item.integration == state.LastSelection {
			return i
		}
	}
	return 0
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil

	case tea.KeyMsg:
		if m.showKB {
			return m.updateKB(msg)
		}
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.quitting = true
			return m, tea.Quit

		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			if m.showOthers && m.cursor < primaryMenuItemCount(m.state) {
				m.showOthers = false
				m.items = buildMenuItems(m.state, false)
				m.cursor = min(m.cursor, len(m.items)-1)
			}
			return m, nil

		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
			if m.cursor < len(m.items) && m.items[m.cursor].isOthers && !m.showOthers {
				m.showOthers = true
				m.items = buildMenuItems(m.state, true)
			}
			return m, nil

		case "enter", " ":
			item := m.items[m.cursor]
			if item.isKnowledge {
				m.showKB = true
				m.kbText = kbStatusText()
				return m, nil
			}
			if m.selectableItem(item) {
				m.selected = true
				m.action = actionForMenuItem(m.items[m.cursor], false)
				m.quitting = true
				return m, tea.Quit
			}
			return m, nil

		case "right", "l", "tab":
			item := m.items[m.cursor]
			if item.isRunModel || m.changeableItem(item) {
				m.selected = true
				m.action = actionForMenuItem(item, true)
				m.quitting = true
				return m, tea.Quit
			}
			return m, nil
		}
	}

	return m, nil
}

// updateKB handles keys on the Knowledge base / RAG sub-screen.
func (m model) updateKB(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		m.quitting = true
		return m, tea.Quit
	case "esc", "b": // back to the main menu
		m.showKB = false
		return m, nil
	case "i": // index now (synchronous; local KBs are small)
		dir, err := knowledge.Dir()
		if err != nil {
			m.kbText = fmt.Sprintf("Cannot open knowledge dir: %v", err)
		} else if st, err := knowledge.Index(dir); err != nil {
			m.kbText = fmt.Sprintf("Index error: %v", err)
		} else {
			m.kbText = formatKBStatus(st) + "\n\nIndexed."
		}
		return m, nil
	case "o": // open folder in the file manager
		if dir, err := knowledge.Dir(); err == nil {
			go openInFileManager(dir)
		}
		return m, nil
	}
	return m, nil
}

func kbStatusText() string {
	dir, err := knowledge.Dir()
	if err != nil {
		return fmt.Sprintf("Cannot open knowledge dir: %v", err)
	}
	st, err := knowledge.IndexStatus(dir)
	if err != nil {
		return fmt.Sprintf("Cannot read index status: %v", err)
	}
	return formatKBStatus(st)
}

func formatKBStatus(st *knowledge.Status) string {
	state := "up to date"
	if st.Stale {
		state = "STALE (files changed on disk — press i to reindex)"
	}
	indexedAt := "never"
	if !st.IndexedAt.IsZero() {
		indexedAt = st.IndexedAt.Local().Format("2006-01-02 15:04")
	}
	return fmt.Sprintf(
		"Files: %d    Chunks: %d\nIndex last updated: %s\nState: %s",
		st.Files, st.Chunks, indexedAt, state)
}

func openInFileManager(dir string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", dir)
	case "windows":
		cmd = exec.Command("explorer", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	_ = cmd.Start()
}

func (m model) selectableItem(item menuItem) bool {
	if item.isRunModel || item.isKnowledge {
		return true
	}
	if item.integration == "" {
		return false
	}
	state, ok := m.state.Integrations[item.integration]
	return ok && state.Selectable
}

func (m model) changeableItem(item menuItem) bool {
	if item.integration == "" {
		return false
	}
	state, ok := m.state.Integrations[item.integration]
	return ok && state.Changeable
}

func (m model) View() string {
	if m.quitting {
		return ""
	}

	s := selectorTitleStyle.Render("Ollama TUI "+versionStyle.Render(version.Version)) + "\n\n"

	if m.showKB {
		s += menuSelectedItemStyle.Render("Knowledge base / RAG") + "\n\n" +
			menuItemStyle.Render(m.kbText) + "\n\n" +
			selectorHelpStyle.Render("i index now • o open folder • esc back to menu • Ctrl+C quit")
		if m.width > 0 {
			return lipgloss.NewStyle().MaxWidth(m.width).Render(s)
		}
		return s
	}

	for i, item := range m.items {
		s += m.renderMenuItem(i, item)
	}

	s += "\n" + selectorHelpStyle.Render("↑/↓ navigate • enter launch • → / Tab. Select AI model. • Ctrl + C quit")

	if m.width > 0 {
		return lipgloss.NewStyle().MaxWidth(m.width).Render(s)
	}
	return s
}

func (m model) renderMenuItem(index int, item menuItem) string {
	cursor := ""
	style := menuItemStyle
	title := item.title
	description := item.description
	modelSuffix := ""

	if m.cursor == index {
		cursor = "▸ "
	}

	if item.isRunModel {
		if m.cursor == index && m.state.RunModel != "" {
			modelSuffix = " " + modelStyle.Render("("+m.state.RunModel+")")
		}
		if m.cursor == index {
			style = menuSelectedItemStyle
		}
	} else if item.isOthers {
		// More immediately expands when reached, so it always uses the default style.
	} else if item.isKnowledge {
		if m.cursor == index {
			style = menuSelectedItemStyle
		}
	} else {
		integrationState := m.state.Integrations[item.integration]
		if !integrationState.Selectable {
			if m.cursor == index {
				style = greyedSelectedStyle
			} else {
				style = greyedStyle
			}
		} else if m.cursor == index {
			style = menuSelectedItemStyle
		}

		if m.cursor == index && integrationState.CurrentModel != "" {
			modelSuffix = " " + modelStyle.Render("("+integrationState.CurrentModel+")")
		}

		if !integrationState.Installed {
			if integrationState.AutoInstallable {
				title += " " + notInstalledStyle.Render("(install)")
			} else {
				title += " " + notInstalledStyle.Render("(not installed)")
			}
			if m.cursor == index {
				if integrationState.AutoInstallable {
					description = "Press enter to install"
				} else if integrationState.InstallHint != "" {
					description = integrationState.InstallHint
				} else {
					description = "not installed"
				}
			}
		}
	}

	return style.Render(cursor+title) + modelSuffix + "\n" + menuDescStyle.Render(description) + "\n\n"
}

type TUIActionKind int

const (
	TUIActionNone TUIActionKind = iota
	TUIActionRunModel
	TUIActionLaunchIntegration
	TUIActionKnowledge
)

type TUIAction struct {
	Kind           TUIActionKind
	Integration    string
	ForceConfigure bool
}

func (a TUIAction) LastSelection() string {
	switch a.Kind {
	case TUIActionRunModel:
		return "run"
	case TUIActionLaunchIntegration:
		return a.Integration
	case TUIActionKnowledge:
		return "knowledge"
	default:
		return ""
	}
}

func (a TUIAction) RunModelRequest() launch.RunModelRequest {
	return launch.RunModelRequest{ForcePicker: a.ForceConfigure}
}

func (a TUIAction) IntegrationLaunchRequest() launch.IntegrationLaunchRequest {
	return launch.IntegrationLaunchRequest{
		Name:           a.Integration,
		ForceConfigure: a.ForceConfigure,
	}
}

func actionForMenuItem(item menuItem, forceConfigure bool) TUIAction {
	switch {
	case item.isRunModel:
		return TUIAction{Kind: TUIActionRunModel, ForceConfigure: forceConfigure}
	case item.isKnowledge:
		return TUIAction{Kind: TUIActionKnowledge}
	case item.integration != "":
		return TUIAction{Kind: TUIActionLaunchIntegration, Integration: item.integration, ForceConfigure: forceConfigure}
	default:
		return TUIAction{Kind: TUIActionNone}
	}
}

func RunMenu(state *launch.LauncherState) (TUIAction, error) {
	menu := newModel(state)
	program := tea.NewProgram(menu)

	finalModel, err := program.Run()
	if err != nil {
		return TUIAction{Kind: TUIActionNone}, fmt.Errorf("error running TUI: %w", err)
	}

	finalMenu := finalModel.(model)
	if !finalMenu.selected {
		return TUIAction{Kind: TUIActionNone}, nil
	}

	return finalMenu.action, nil
}
