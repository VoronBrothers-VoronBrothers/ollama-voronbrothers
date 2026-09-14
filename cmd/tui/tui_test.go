package tui

import (
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

func TestMenuRendersOnlyRunChoice(t *testing.T) {
	state := launcherTestState()
	menu := newModel(state)
	want := []string{"run"}
	if diff := compareStrings(integrationSequence(menu.items), want); diff != "" {
		t.Fatalf("unexpected root launch choices: %s", diff)
	}

	view := menu.View()
	for _, want := range []string{
		"Chat with a model",
		"Start an interactive chat with a model",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected menu view to contain %q\n%s", want, view)
		}
	}
	for _, hidden := range []string{
		"Launch Claude Code", "Launch OpenCode", "Launch Hermes Agent",
		"Launch OpenClaw", "More...",
	} {
		if strings.Contains(view, hidden) {
			t.Fatalf("expected menu to omit %q\n%s", hidden, view)
		}
	}
}

func TestMenuNavigationStaysOnRun(t *testing.T) {
	menu := newModel(launcherTestState())
	for _, msg := range []tea.KeyMsg{
		{Type: tea.KeyDown}, {Type: tea.KeyDown}, {Type: tea.KeyUp}, {Type: tea.KeyUp},
	} {
		updated, _ := menu.Update(msg)
		menu = updated.(model)
		if menu.cursor != 0 || !menu.items[0].isRunModel {
			t.Fatalf("expected cursor to stay on run item, got cursor=%d", menu.cursor)
		}
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

func TestMenuShowsCurrentModelSuffixOnRun(t *testing.T) {
	menu := newModel(launcherTestState())
	view := menu.View()
	if !strings.Contains(view, "(qwen3:8b)") {
		t.Fatalf("expected run row to show current model suffix\n%s", view)
	}
}
