package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/go-cmp/cmp"
	"github.com/ollama/ollama/cmd/launch"
)

func launcherTestState() *launch.LauncherState {
	return &launch.LauncherState{
		LastSelection: "run",
		RunModel:      "qwen3:8b",
		Integrations: map[string]launch.LauncherIntegrationState{
			"claude": {
				Name:         "claude",
				DisplayName:  "Claude Code",
				Description:  "Anthropic's coding tool with subagents",
				Selectable:   true,
				Changeable:   true,
				CurrentModel: "glm-5:cloud",
			},
			"codex": {
				Name:        "codex",
				DisplayName: "Codex CLI",
				Description: "OpenAI's open-source coding agent",
				Selectable:  true,
				Changeable:  true,
			},
			"chatgpt": {
				Name:        "chatgpt",
				DisplayName: "ChatGPT",
				Description: "Complete work with ChatGPT",
				Selectable:  true,
				Changeable:  true,
			},
			"openclaw": {
				Name:            "openclaw",
				DisplayName:     "OpenClaw",
				Description:     "Personal AI with 100+ skills",
				Selectable:      true,
				Changeable:      true,
				AutoInstallable: true,
			},
			"opencode": {
				Name:        "opencode",
				DisplayName: "OpenCode",
				Description: "Anomaly's open-source coding agent",
				Selectable:  true,
				Changeable:  true,
			},
			"hermes": {
				Name:        "hermes",
				DisplayName: "Hermes Agent",
				Description: "Self-improving AI agent built by Nous Research",
				Selectable:  true,
				Changeable:  true,
			},
			"droid": {
				Name:        "droid",
				DisplayName: "Droid",
				Description: "Factory's coding agent across terminal and IDEs",
				Selectable:  true,
				Changeable:  true,
			},
			"pi": {
				Name:        "pi",
				DisplayName: "Pi",
				Description: "Minimal AI agent toolkit with plugin support",
				Selectable:  true,
				Changeable:  true,
			},
		},
	}
}

func findMenuCursorByIntegration(items []menuItem, name string) int {
	for i, item := range items {
		if item.integration == name {
			return i
		}
	}
	return -1
}

func integrationSequence(items []menuItem) []string {
	sequence := make([]string, 0, len(items))
	for _, item := range items {
		switch {
		case item.isRunModel:
			sequence = append(sequence, "run")
		case item.isKnowledge:
			sequence = append(sequence, "kb")
		case item.isOthers:
			sequence = append(sequence, "more")
		case item.integration != "":
			sequence = append(sequence, item.integration)
		}
	}
	return sequence
}

func compareStrings(got, want []string) string {
	return cmp.Diff(want, got)
}

// The menu always starts with [run, kb], then up to launcherMenuLimit integrations, then More.

func TestMenuPromotesInstalledAppsInDefaultPriorityOrder(t *testing.T) {
	for _, tc := range []struct {
		name         string
		installed    []string
		want         []string
		wantOverflow []string
	}{
		{"none installed", nil, []string{"run", "kb", "claude", "opencode", "hermes", "openclaw", "more"}, []string{"codex", "droid", "pi"}},
		{"only OpenClaw", []string{"openclaw"}, []string{"run", "kb", "openclaw", "claude", "opencode", "hermes", "more"}, []string{"codex", "droid", "pi"}},
		{"installed primary and additional apps", []string{"pi", "codex", "openclaw"}, []string{"run", "kb", "openclaw", "codex", "pi", "claude", "more"}, []string{"opencode", "hermes", "droid"}},
		{"more than four installed", []string{"pi", "droid", "codex", "openclaw", "hermes", "opencode"}, []string{"run", "kb", "opencode", "hermes", "openclaw", "codex", "more"}, []string{"droid", "pi", "claude"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := launcherTestState()
			delete(state.Integrations, "chatgpt")
			for _, name := range tc.installed {
				app := state.Integrations[name]
				app.Installed = true
				state.Integrations[name] = app
			}
			menu := newModel(state)
			if diff := compareStrings(integrationSequence(menu.items), tc.want); diff != "" {
				t.Fatalf("wrong installed-first order: %s", diff)
			}
			expanded := buildMenuItems(state, true)
			// offset: 2 fixed items (run+kb) + launcherMenuLimit
			if diff := compareStrings(integrationSequence(expanded[2+launcherMenuLimit:]), tc.wantOverflow); diff != "" {
				t.Fatalf("remaining apps should stay under More in priority order: %s", diff)
			}
		})
	}
}

func TestMenuRemembersPromotedAppWithoutOpeningMore(t *testing.T) {
	state := launcherTestState()
	app := state.Integrations["codex"]
	app.Installed = true
	state.Integrations["codex"] = app
	state.LastSelection = "codex"
	menu := newModel(state)
	// codex is at position 2 (after run+kb), promoted to first integration slot
	if menu.showOthers || menu.cursor != 2 || menu.items[menu.cursor].integration != "codex" {
		t.Fatal("previously selected installed app should be recalled in its promoted position")
	}
}

func TestMenuWithoutAppsOnlyOffersChatAndKB(t *testing.T) {
	menu := newModel(nil)
	if diff := compareStrings(integrationSequence(menu.items), []string{"run", "kb"}); diff != "" {
		t.Fatalf("menu without apps should only offer chat and kb: %s", diff)
	}
	if view := menu.View(); !strings.Contains(view, "Chat with a model") {
		t.Fatalf("expected chat item in view\n%s", view)
	}
	if view := menu.View(); !strings.Contains(view, "Knowledge base / RAG") {
		t.Fatalf("expected knowledge item in view\n%s", view)
	}
	updated, _ := menu.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := updated.(model); !got.selected || got.action != (TUIAction{Kind: TUIActionRunModel}) {
		t.Fatalf("enter should start a chat, got selected=%v action=%v", got.selected, got.action)
	}
}

func TestMenuUpToFourAppsDoesNotNeedMore(t *testing.T) {
	for _, names := range [][]string{
		{"claude", "opencode", "hermes"},
		{"claude", "opencode", "hermes", "openclaw"},
	} {
		state := launcherTestState()
		apps := make(map[string]launch.LauncherIntegrationState)
		for _, name := range names {
			apps[name] = state.Integrations[name]
		}
		state.Integrations = apps
		menu := newModel(state)
		want := append([]string{"run", "kb"}, names...)
		if diff := compareStrings(integrationSequence(menu.items), want); diff != "" {
			t.Fatalf("up to four apps should appear directly without More: %s", diff)
		}
	}
}

func TestMenuExpandsAndCollapsesMoreAfterPromotion(t *testing.T) {
	state := launcherTestState()
	app := state.Integrations["codex"]
	app.Installed = true
	state.Integrations["codex"] = app
	menu := newModel(state)
	// More is at position 2 + launcherMenuLimit (after run+kb + 4 apps); set cursor to last app before it.
	lastPrimaryIdx := 2 + launcherMenuLimit - 1 // index of 4th integration
	menu.cursor = lastPrimaryIdx
	root := integrationSequence(menu.items)

	updated, _ := menu.Update(tea.KeyMsg{Type: tea.KeyDown})
	expanded := updated.(model)
	if !expanded.showOthers || expanded.cursor != lastPrimaryIdx+1 || expanded.items[expanded.cursor].integration == "" {
		t.Fatal("Down must expand More and select the first overflow app")
	}
	updated, _ = expanded.Update(tea.KeyMsg{Type: tea.KeyUp})
	collapsed := updated.(model)
	if collapsed.showOthers || collapsed.cursor != lastPrimaryIdx {
		t.Fatal("Up must return to the last primary app")
	}
	if diff := compareStrings(integrationSequence(collapsed.items), root); diff != "" {
		t.Fatalf("collapsing More changed the promoted root menu: %s", diff)
	}
}

func TestMenuStartsExpandedForPreviousOverflowSelection(t *testing.T) {
	state := launcherTestState()
	fullItems := buildMenuItems(state, true)
	overflow := fullItems[2+launcherMenuLimit:] // skip run+kb + limit apps
	if len(overflow) < 2 {
		t.Fatal("expected at least two additional integrations")
	}
	state.LastSelection = overflow[1].integration

	menu := newModel(state)
	if !menu.showOthers {
		t.Fatal("expected previous additional integration selection to start expanded")
	}
	if got := menu.items[menu.cursor].integration; got != state.LastSelection {
		t.Fatalf("initial cursor integration = %q, want %q", got, state.LastSelection)
	}
	for _, item := range menu.items {
		if item.isOthers {
			t.Fatal("expanded menu must contain apps instead of More")
		}
	}
}

func TestMenuRemembersChat(t *testing.T) {
	state := launcherTestState()
	state.LastSelection = "run"
	menu := newModel(state)
	if menu.cursor != 0 || !menu.items[menu.cursor].isRunModel {
		t.Fatal("previous chat selection should start on Chat with a model")
	}
}

func TestMenuEnterOnRunSelectsRun(t *testing.T) {
	menu := newModel(launcherTestState())
	updated, _ := menu.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(model)
	want := TUIAction{Kind: TUIActionRunModel}
	if !got.selected || got.action != want {
		t.Fatalf("expected enter on run to select run action, got selected=%v action=%v", got.selected, got.action)
	}
}

func TestMenuRightOnRunSelectsChangeRun(t *testing.T) {
	menu := newModel(launcherTestState())
	updated, _ := menu.Update(tea.KeyMsg{Type: tea.KeyRight})
	got := updated.(model)
	want := TUIAction{Kind: TUIActionRunModel, ForceConfigure: true}
	if !got.selected || got.action != want {
		t.Fatalf("expected right on run to select change-run action, got selected=%v action=%v", got.selected, got.action)
	}
}

func TestMenuTabOnRunSelectsChangeRun(t *testing.T) {
	menu := newModel(launcherTestState())
	updated, _ := menu.Update(tea.KeyMsg{Type: tea.KeyTab})
	got := updated.(model)
	want := TUIAction{Kind: TUIActionRunModel, ForceConfigure: true}
	if !got.selected || got.action != want {
		t.Fatalf("expected tab on run to select change-run action, got selected=%v action=%v", got.selected, got.action)
	}
}

func TestMenuEnterOnIntegrationSelectsLaunch(t *testing.T) {
	menu := newModel(launcherTestState())
	menu.cursor = findMenuCursorByIntegration(menu.items, "claude")
	if menu.cursor == -1 {
		t.Fatal("expected claude menu item")
	}
	updated, _ := menu.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(model)
	want := TUIAction{Kind: TUIActionLaunchIntegration, Integration: "claude"}
	if !got.selected || got.action != want {
		t.Fatalf("expected enter on integration to launch, got selected=%v action=%v", got.selected, got.action)
	}
}

func TestMenuRightOnIntegrationSelectsConfigure(t *testing.T) {
	menu := newModel(launcherTestState())
	menu.cursor = findMenuCursorByIntegration(menu.items, "claude")
	if menu.cursor == -1 {
		t.Fatal("expected claude menu item")
	}
	updated, _ := menu.Update(tea.KeyMsg{Type: tea.KeyRight})
	got := updated.(model)
	want := TUIAction{Kind: TUIActionLaunchIntegration, Integration: "claude", ForceConfigure: true}
	if !got.selected || got.action != want {
		t.Fatalf("expected right on integration to configure, got selected=%v action=%v", got.selected, got.action)
	}
}

func TestMenuIgnoresDisabledActions(t *testing.T) {
	state := launcherTestState()
	claude := state.Integrations["claude"]
	claude.Selectable = false
	claude.Changeable = false
	state.Integrations["claude"] = claude

	menu := newModel(state)
	menu.cursor = findMenuCursorByIntegration(menu.items, "claude")
	if menu.cursor == -1 {
		t.Fatal("expected claude menu item")
	}

	updatedEnter, _ := menu.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if updatedEnter.(model).selected {
		t.Fatal("expected non-selectable integration to ignore enter")
	}

	updatedRight, _ := menu.Update(tea.KeyMsg{Type: tea.KeyRight})
	if updatedRight.(model).selected {
		t.Fatal("expected non-changeable integration to ignore right")
	}
}

func TestMenuShowsOnlySelectedItemsCurrentModel(t *testing.T) {
	state := launcherTestState()
	menu := newModel(state)
	for i, item := range menu.items {
		menu.cursor = i
		view := menu.View()
		if got, want := strings.Contains(view, state.RunModel), item.isRunModel; got != want {
			t.Fatalf("chat model visibility at cursor %d = %v, want %v", i, got, want)
		}
		if got, want := strings.Contains(view, state.Integrations["claude"].CurrentModel), item.integration == "claude"; got != want {
			t.Fatalf("current model visibility at cursor %d = %v, want %v", i, got, want)
		}
	}
}

func TestMenuShowsInstallStatusAndHint(t *testing.T) {
	state := launcherTestState()
	opencode := state.Integrations["opencode"]
	opencode.Installed = false
	opencode.Selectable = false
	opencode.Changeable = false
	opencode.InstallHint = "Install from https://example.com/opencode"
	state.Integrations["opencode"] = opencode

	state.LastSelection = "opencode"
	menu := newModel(state)
	menu.cursor = findMenuCursorByIntegration(menu.items, "opencode")
	if menu.cursor == -1 {
		t.Fatal("expected opencode menu item")
	}
	view := menu.View()
	if !strings.Contains(view, "(not installed)") {
		t.Fatalf("expected not-installed marker\n%s", view)
	}
	if !strings.Contains(view, opencode.InstallHint) {
		t.Fatalf("expected install hint in description\n%s", view)
	}
}

func TestMenuNavigationCyclesRootItems(t *testing.T) {
	menu := newModel(launcherTestState())
	// Root menu: run item (0) and knowledge item (1).
	updated, _ := menu.Update(tea.KeyMsg{Type: tea.KeyDown})
	menu = updated.(model)
	if menu.cursor != 1 || !menu.items[1].isKnowledge {
		t.Fatalf("expected cursor on knowledge item after down, got %d", menu.cursor)
	}
	updated, _ = menu.Update(tea.KeyMsg{Type: tea.KeyUp})
	menu = updated.(model)
	if menu.cursor != 0 || !menu.items[0].isRunModel {
		t.Fatalf("expected cursor back on run item after up, got %d", menu.cursor)
	}
}

func TestMenuKnowledgeScreenShowsStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	kbDir := filepath.Join(home, ".ollama", "knowledge")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kbDir, "a.md"), []byte("# t\n\ntext here\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	menu := newModel(launcherTestState())
	// Move to the knowledge item and enter it.
	updated, _ := menu.Update(tea.KeyMsg{Type: tea.KeyDown})
	menu = updated.(model)
	updated, _ = menu.Update(tea.KeyMsg{Type: tea.KeyEnter})
	menu = updated.(model)
	if !menu.showKB {
		t.Fatal("expected knowledge screen to open")
	}
	view := menu.View()
	for _, want := range []string{"Files:", "Chunks:"} {
		if !strings.Contains(view, want) {
			t.Fatalf("knowledge screen missing %q\n%s", want, view)
		}
	}

	// esc returns to the main menu.
	updated, _ = menu.Update(tea.KeyMsg{Type: tea.KeyEsc})
	menu = updated.(model)
	if menu.showKB {
		t.Fatal("expected back on main menu after esc")
	}
}
