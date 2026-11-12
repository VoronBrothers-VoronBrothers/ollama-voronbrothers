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

func integrationSequence(items []menuItem) []string {
	sequence := make([]string, 0, len(items))
	for _, item := range items {
		switch {
		case item.isRunModel:
			sequence = append(sequence, "run")
		case item.isKnowledge:
			sequence = append(sequence, "kb")
		case item.isService:
			sequence = append(sequence, "service")
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

func TestMenuHasExactlyThreeFixedItems(t *testing.T) {
	menu := newModel(nil)
	if diff := compareStrings(integrationSequence(menu.items), []string{"run", "kb", "service"}); diff != "" {
		t.Fatalf("menu must be exactly chat + knowledge + service: %s", diff)
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

// withIntegrationItem appends an integration entry to the menu and puts the
// cursor on it. The fixed 3-item menu does not show integrations, but the
// launch path is still reachable (CLI / future menu layouts), so the tests
// drive it through the model directly.
func withIntegrationItem(menu model, state *launch.LauncherState, name string) model {
	menu.items = append(menu.items, integrationMenuItem(state.Integrations[name]))
	menu.cursor = len(menu.items) - 1
	return menu
}

func TestMenuEnterOnIntegrationSelectsLaunch(t *testing.T) {
	state := launcherTestState()
	menu := withIntegrationItem(newModel(state), state, "claude")
	updated, _ := menu.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(model)
	want := TUIAction{Kind: TUIActionLaunchIntegration, Integration: "claude"}
	if !got.selected || got.action != want {
		t.Fatalf("expected enter on integration to launch, got selected=%v action=%v", got.selected, got.action)
	}
}

func TestMenuRightOnIntegrationSelectsConfigure(t *testing.T) {
	state := launcherTestState()
	menu := withIntegrationItem(newModel(state), state, "claude")
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

	menu := withIntegrationItem(newModel(state), state, "claude")

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
	menu := withIntegrationItem(newModel(state), state, "claude")
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

	menu := withIntegrationItem(newModel(state), state, "opencode")
	view := menu.View()
	if !strings.Contains(view, "(not installed)") {
		t.Fatalf("expected not-installed marker\n%s", view)
	}
	if !strings.Contains(view, opencode.InstallHint) {
		t.Fatalf("expected install hint in description\n%s", view)
	}
}

func TestMenuNavigationWalksAllFixedItems(t *testing.T) {
	menu := newModel(launcherTestState())
	// Root menu: chat (0), knowledge (1), service (2).
	updated, _ := menu.Update(tea.KeyMsg{Type: tea.KeyDown})
	menu = updated.(model)
	if menu.cursor != 1 || !menu.items[1].isKnowledge {
		t.Fatalf("expected cursor on knowledge item after down, got %d", menu.cursor)
	}
	updated, _ = menu.Update(tea.KeyMsg{Type: tea.KeyDown})
	menu = updated.(model)
	if menu.cursor != 2 || !menu.items[2].isService {
		t.Fatalf("expected cursor on service item after second down, got %d", menu.cursor)
	}
	updated, _ = menu.Update(tea.KeyMsg{Type: tea.KeyDown})
	menu = updated.(model)
	if menu.cursor != 2 {
		t.Fatalf("down past the last item must not move the cursor, got %d", menu.cursor)
	}
	updated, _ = menu.Update(tea.KeyMsg{Type: tea.KeyUp})
	menu = updated.(model)
	if menu.cursor != 1 || !menu.items[1].isKnowledge {
		t.Fatalf("expected cursor back on knowledge item after up, got %d", menu.cursor)
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

func TestMenuServiceScreenRendersEnglishAndSelectsFirstItem(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	menu := newModel(nil)
	if view := menu.View(); !strings.Contains(view, "Service") {
		t.Fatalf("expected english Service item in main menu\n%s", view)
	}

	// Move to the service item and enter it.
	updated, _ := menu.Update(tea.KeyMsg{Type: tea.KeyDown})
	menu = updated.(model)
	updated, _ = menu.Update(tea.KeyMsg{Type: tea.KeyDown})
	menu = updated.(model)
	if !menu.items[menu.cursor].isService {
		t.Fatalf("expected cursor on service item, got %q", menu.items[menu.cursor].title)
	}
	updated, _ = menu.Update(tea.KeyMsg{Type: tea.KeyEnter})
	menu = updated.(model)
	if !menu.showService {
		t.Fatal("expected service screen to open")
	}
	if menu.serviceCursor != 0 {
		t.Fatalf("first service entry must be pre-selected, cursor=%d", menu.serviceCursor)
	}

	view := menu.View()
	for _, want := range []string{"Service", "Recreate env vars (~/.ollama/env)", "▸"} {
		if !strings.Contains(view, want) {
			t.Fatalf("service screen missing %q\n%s", want, view)
		}
	}
	if strings.Contains(view, "Служебное") {
		t.Fatalf("service screen must be english only\n%s", view)
	}
}

func TestMenuServiceConfirmWritesReferenceEnvFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	menu := newModel(nil)
	menu.showService = true
	menu.serviceCursor = 0

	updated, _ := menu.Update(tea.KeyMsg{Type: tea.KeyEnter})
	menu = updated.(model)
	if !menu.showEnvConfirm {
		t.Fatal("expected confirmation prompt")
	}

	updated, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	menu = updated.(model)
	if menu.envMsg == "" {
		t.Fatal("expected result message after confirm")
	}

	data, err := os.ReadFile(filepath.Join(home, ".ollama", "env"))
	if err != nil {
		t.Fatalf("env file not written: %v", err)
	}
	if !strings.Contains(string(data), "OLLAMA_KEEP_ALIVE") {
		t.Fatalf("env reference missing known variables\n%s", string(data))
	}
}

func TestMenuServiceSubscreenNavigatesBack(t *testing.T) {
	menu := newModel(nil)
	menu.showService = true
	menu.serviceCursor = 0

	// A single entry: down/up must not push the cursor out of range.
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyDown},
		{Type: tea.KeyDown},
		{Type: tea.KeyUp},
	} {
		updated, _ := menu.Update(key)
		menu = updated.(model)
		if menu.serviceCursor != 0 {
			t.Fatalf("cursor on the only entry must stay 0, got %d", menu.serviceCursor)
		}
	}

	updated, _ := menu.Update(tea.KeyMsg{Type: tea.KeyEsc})
	menu = updated.(model)
	if menu.showService {
		t.Fatal("esc must return to the main menu")
	}
	if view := menu.View(); !strings.Contains(view, "Chat with a model") {
		t.Fatalf("expected main menu after esc\n%s", view)
	}
}

func TestMenuServiceConfirmCancelAndRepeatAreSafe(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	menu := newModel(nil)
	menu.showService = true

	// enter -> confirm prompt, n cancels without writing.
	updated, _ := menu.Update(tea.KeyMsg{Type: tea.KeyEnter})
	menu = updated.(model)
	updated, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	menu = updated.(model)
	if menu.showEnvConfirm || menu.envMsg != "" {
		t.Fatal("n must close the prompt and clear the message")
	}
	if _, err := os.Stat(filepath.Join(home, ".ollama", "env")); !os.IsNotExist(err) {
		t.Fatal("cancel must not write the env file")
	}

	// Confirm, then press y again: the file must not be rewritten/duplicated.
	updated, _ = menu.Update(tea.KeyMsg{Type: tea.KeyEnter})
	menu = updated.(model)
	updated, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	menu = updated.(model)
	first := menu.envMsg
	updated, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	menu = updated.(model)
	if menu.envMsg != first {
		t.Fatalf("repeated confirm must keep the first result, got %q want %q", menu.envMsg, first)
	}

	// esc leaves the confirmation state and then the service screen.
	updated, _ = menu.Update(tea.KeyMsg{Type: tea.KeyEsc})
	menu = updated.(model)
	if menu.showEnvConfirm {
		t.Fatal("esc must leave the confirmation state")
	}
}

func TestMenuServiceShowsWriteResult(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	menu := newModel(nil)
	menu.showService = true

	updated, _ := menu.Update(tea.KeyMsg{Type: tea.KeyEnter})
	menu = updated.(model)
	if view := menu.View(); !strings.Contains(view, "Write to: "+filepath.Join(home, ".ollama", "env")) {
		t.Fatalf("confirmation must show the real target path\n%s", view)
	}

	updated, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	menu = updated.(model)
	view := menu.View()
	for _, want := range []string{"Written:", filepath.Join(home, ".ollama", "env")} {
		if !strings.Contains(view, want) {
			t.Fatalf("result screen missing %q\n%s", want, view)
		}
	}
}

func TestMenuServiceReportsWriteError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A regular file where the .ollama directory should be makes MkdirAll fail.
	if err := os.WriteFile(filepath.Join(home, ".ollama"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	menu := newModel(nil)
	menu.showService = true

	updated, _ := menu.Update(tea.KeyMsg{Type: tea.KeyEnter})
	menu = updated.(model)
	updated, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	menu = updated.(model)

	if !strings.HasPrefix(menu.envMsg, "Error: ") {
		t.Fatalf("expected an error message, got %q", menu.envMsg)
	}
	if view := menu.View(); !strings.Contains(view, "Error: ") {
		t.Fatalf("error must be visible\n%s", view)
	}
}
