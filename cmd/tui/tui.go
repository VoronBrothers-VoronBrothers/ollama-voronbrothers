package tui

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ollama/ollama/cmd/launch"
	"github.com/ollama/ollama/envconfig"
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
	isService   bool
}

var runModelMenuItem = menuItem{
	title:       "Chat with a model",
	description: "Start an interactive chat with a model",
	isRunModel:  true,
}

var knowledgeMenuItem = menuItem{
	title:       "Knowledge base / RAG",
	description: "Search local documents (.txt/.md): index status, reindex, open folder",
	isKnowledge: true,
}

var serviceMenuItem = menuItem{
	title:       "Служебное",
	description: "Service tools: env vars, diagnostics, maintenance",
	isService:   true,
}

var othersMenuItem = menuItem{
	title:       "More...",
	description: "Show additional integrations",
	isOthers:    true,
}

// launcherMenuIntegrations defines the default priority before the additional
// integrations in registry order. Chat with a model comes first, then installed
// apps are promoted above missing apps, with only the first four shown before More.
var launcherMenuIntegrations = []string{"claude", "opencode", "hermes", "openclaw"}

const launcherMenuLimit = 4

type model struct {
	state         *launch.LauncherState
	items         []menuItem
	cursor        int
	showOthers    bool
	width         int
	quitting      bool
	selected      bool
	action        TUIAction
	showKB        bool
	kbText        string
	showService   bool
	serviceCursor int
	showEnvConfirm bool
	envMsg        string
}

func newModel(state *launch.LauncherState) model {
	if state == nil {
		state = &launch.LauncherState{}
	}
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
	for i, item := range orderedIntegrationItems(state) {
		if i >= launcherMenuLimit && item.integration == state.LastSelection {
			return true
		}
	}
	return false
}

func buildMenuItems(state *launch.LauncherState, showOthers bool) []menuItem {
	// Fixed 3-item menu (old releases): Chat / Knowledge base-RAG / Служебное.
	// Upstream launch integrations (Claude Code, Qwen Code, etc.) are NOT shown
	// in the top-level TUI menu — they stay reachable via `ollama launch` CLI only.
	return []menuItem{runModelMenuItem, knowledgeMenuItem, serviceMenuItem}
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

func orderedIntegrationItems(state *launch.LauncherState) []menuItem {
	if state == nil {
		return nil
	}

	var items []menuItem
	for _, name := range launcherMenuIntegrations {
		integrationState, ok := state.Integrations[name]
		if !ok {
			continue
		}
		items = append(items, integrationMenuItem(integrationState))
	}
	for _, info := range launch.ListIntegrationInfos() {
		if slices.Contains(launcherMenuIntegrations, info.Name) {
			continue
		}
		integrationState, ok := state.Integrations[info.Name]
		if !ok {
			continue
		}
		items = append(items, integrationMenuItem(integrationState))
	}

	var installed, missing []menuItem
	for _, item := range items {
		if state.Integrations[item.integration].Installed {
			installed = append(installed, item)
		} else {
			missing = append(missing, item)
		}
	}
	return append(installed, missing...)
}

func initialCursor(state *launch.LauncherState, items []menuItem) int {
	if state == nil || state.LastSelection == "" {
		return 0
	}
	for i, item := range items {
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
		if m.showService {
			return m.updateService(msg)
		}
		switch msg.String() {
		case "ctrl+c", "q":
			m.quitting = true
			return m, tea.Quit

		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			if m.showOthers && m.cursor <= launcherMenuLimit+1 {
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
			if item.isService {
				m.showService = true
				m.serviceCursor = 0
				return m, nil
			}
			if m.selectableItem(item) {
				m.selected = true
				m.action = actionForMenuItem(item, false)
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

		case "esc":
			// In main menu: quit (consistent with upstream behaviour for esc at root).
			m.quitting = true
			return m, tea.Quit
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
			return m, nil
		}
		st, err := knowledge.Index(dir)
		if err != nil {
			m.kbText = fmt.Sprintf("Index error: %v", err)
			return m, nil
		}
		m.kbText = fmt.Sprintf("Indexed.\n\n%s", formatKBStatus(st))
		return m, nil
	case "o": // open folder in file manager
		dir, _ := knowledge.Dir()
		var cmd *exec.Cmd
		if runtime.GOOS == "darwin" {
			cmd = exec.Command("open", dir)
		} else if runtime.GOOS == "windows" {
			cmd = exec.Command("explorer", dir)
		} else {
			cmd = exec.Command("xdg-open", dir)
		}
		_ = cmd.Start()
	}
	return m, nil
}

// updateService handles keys on the Service (Служебное) sub-screen.
func (m model) updateService(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		m.quitting = true
		return m, tea.Quit
	case "esc", "b": // back to main menu
		m.showService = false
		m.showEnvConfirm = false
		return m, nil
	}

	// Confirmation sub-state: y/n for recreate env vars
	if m.showEnvConfirm {
		switch msg.String() {
		case "y", "\r":
			path, err := envconfig.RecreateUserEnvFile()
			if err != nil {
				m.envMsg = "Error: " + err.Error()
			} else {
				m.envMsg = path + " recreated successfully.\nRestart ollama service to apply changes."
			}
			return m, nil
		case "n":
			m.showEnvConfirm = false
			m.envMsg = ""
			return m, nil
		}
		return m, nil
	}

	// Service sub-menu navigation
	const serviceItemsCount = 1 // currently only one item: recreate env vars
	switch msg.String() {
	case "up", "k":
		if m.serviceCursor > 0 {
			m.serviceCursor--
		}
	case "down", "j":
		if m.serviceCursor < serviceItemsCount-1 {
			m.serviceCursor++
		}
	case "enter", " ":
		switch m.serviceCursor {
		case 0: // Recreate env vars
			m.showEnvConfirm = true
		}
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

func (m model) selectableItem(item menuItem) bool {
	if item.isRunModel || item.isKnowledge || item.isService {
		return true
	}
	if item.integration == "" || m.state == nil {
		return false
	}
	state, ok := m.state.Integrations[item.integration]
	return ok && state.Selectable
}

func (m model) changeableItem(item menuItem) bool {
	if item.isRunModel {
		return true
	}
	if item.integration == "" || m.state == nil {
		return false
	}
	state, ok := m.state.Integrations[item.integration]
	return ok && state.Changeable
}

func (m model) View() string {
	if m.quitting {
		return ""
	}

	s := selectorTitleStyle.Render("Ollama TUI " + versionStyle.Render(version.Version)) + "\n\n"

	if m.showKB {
		s += menuSelectedItemStyle.Render("Knowledge base / RAG") + "\n\n" +
			menuItemStyle.Render(m.kbText) + "\n\n" +
			selectorHelpStyle.Render("i index now • o open folder • esc back to menu • Ctrl+C quit")
		if m.width > 0 {
			return lipgloss.NewStyle().MaxWidth(m.width).Render(s)
		}
		return s
	}

	if m.showService {
		s += menuSelectedItemStyle.Render("Служебное") + "\n\n"

		if m.showEnvConfirm {
			if m.envMsg != "" {
				s += menuItemStyle.Render(m.envMsg) + "\n\n"
			} else {
				home, _ := os.UserHomeDir()
				path := home + "/.ollama/env"
				s += menuItemStyle.Render("Recreate env vars: " + path) + "\n" +
					menuItemStyle.Render("This will overwrite the file with documented defaults.\n") +
					menuItemStyle.Render("Your current settings in that file will be LOST.") + "\n\n"
			}
			s += selectorHelpStyle.Render("y confirm • n cancel")
		} else {
			// Service sub-menu items
			serviceItems := []string{"Recreate env vars (~/.ollama/env)"}
			for i, label := range serviceItems {
				prefix := "  "
				if m.serviceCursor == i {
					prefix = "▸ "
				}
				s += menuItemStyle.Render(prefix+label) + "\n"
			}
			s += "\n" + selectorHelpStyle.Render("↑/↓ navigate • enter select • esc back to menu")
		}

		if m.width > 0 {
			return lipgloss.NewStyle().MaxWidth(m.width).Render(s)
		}
		return s
	}

	for i, item := range m.items {
		s += m.renderMenuItem(i, item)
	}

	s += "\n" + selectorHelpStyle.Render("↑/↓ navigate • enter launch • → / Tab configure • Ctrl+C quit")

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
		if m.cursor == index {
			style = menuSelectedItemStyle
			if m.state.RunModel != "" {
				modelSuffix = " " + modelStyle.Render("("+m.state.RunModel+")")
			}
		}
	} else if item.isOthers {
		// More immediately expands when reached, so it always uses the default style.
	} else if item.isKnowledge {
		if m.cursor == index {
			style = menuSelectedItemStyle
		}
	} else if item.isService {
		if m.cursor == index {
			style = menuSelectedItemStyle
		}} else {
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
