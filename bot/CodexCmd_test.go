package main

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestBuildCodexCommandIncludesGPT6SolAndLuna(t *testing.T) {
	command, err := buildCodexCommand()
	if err != nil {
		t.Fatal(err)
	}

	models := map[string]string{}
	for _, option := range command.args {
		if option.Name != "ask" {
			continue
		}
		for _, askOption := range option.Options {
			if askOption.Name != "model" || askOption.Type != discordgo.ApplicationCommandOptionString {
				continue
			}
			for _, choice := range askOption.Choices {
				value, ok := choice.Value.(string)
				if ok {
					models[choice.Name] = value
				}
			}
		}
	}

	for name, expectedID := range map[string]string{
		"GPT-6 Sol":  "gpt-6-sol",
		"GPT-6 Luna": "gpt-6-luna",
	} {
		if models[name] != expectedID {
			t.Errorf("%s model ID = %q, want %q", name, models[name], expectedID)
		}
	}
}

func TestResolveCodexPreferences(t *testing.T) {
	previous := CodexPreferences{ProjectID: "my-project", Model: "gpt-6-sol", Effort: "xhigh"}
	unchanged := resolveCodexPreferences(previous, nil, nil, nil)
	if unchanged != previous {
		t.Fatalf("omitted options changed preferences: got %+v, want %+v", unchanged, previous)
	}

	newModel := "gpt-6-luna"
	changed := resolveCodexPreferences(previous, nil, &newModel, nil)
	if changed.ProjectID != previous.ProjectID || changed.Model != newModel || changed.Effort != previous.Effort {
		t.Fatalf("model change affected other options: %+v", changed)
	}

	defaultOption := "default"
	reset := resolveCodexPreferences(previous, nil, &defaultOption, &defaultOption)
	if reset.ProjectID != previous.ProjectID || reset.Model != "" || reset.Effort != "" {
		t.Fatalf("explicit defaults were not applied: %+v", reset)
	}
}

func TestNormalizeCodexToolName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"cat /mnt/workspace/README.md", "shell"},
		{"git status --porcelain", "shell"},
		{"ls -la", "shell"},
		{"go build -o bot .", "shell"},
		{"commandExecution", "shell"},
		{"bash", "shell"},
		{"shell", "shell"},
		{"apply_patch", "apply_patch"},
		{"view_file", "view_file"},
		{"grep", "shell"},
	}

	for _, tt := range tests {
		got := normalizeCodexToolName(tt.input)
		if got != tt.expected {
			t.Errorf("normalizeCodexToolName(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestFormatCompressedTools_Codex(t *testing.T) {
	tests := []struct {
		tools    []string
		expected string
	}{
		{[]string{}, ""},
		{[]string{"shell"}, "**사용된 도구**: shell(x1)"},
		{[]string{"shell", "shell"}, "**사용된 도구**: shell(x2)"},
		{[]string{"shell", "apply_patch", "shell"}, "**사용된 도구**: shell(x1) -> apply_patch(x1) -> shell(x1)"},
	}

	for _, tt := range tests {
		got := formatCompressedTools(tt.tools)
		if got != tt.expected {
			t.Errorf("formatCompressedTools(%v) = %q, want %q", tt.tools, got, tt.expected)
		}
	}
}

func TestBuildCodexComponents_ToolFooter(t *testing.T) {
	state := &CodexSessionState{
		SessionID:    "test_sess",
		ProjectID:    "ai-agent",
		Prompt:       "테스트 질문",
		Status:       "completed",
		RawToolNames: []string{normalizeCodexToolName("cat /etc/passwd"), normalizeCodexToolName("ls -la")},
	}
	comps := buildCodexComponents(state)
	if len(comps) == 0 {
		t.Fatalf("expected components")
	}

	// Verify the footer does not leak the command and contains shell(x2).
	footer := formatCompressedTools(state.RawToolNames)
	if !strings.Contains(footer, "shell(x2)") {
		t.Errorf("expected shell(x2) in tools, got %s", footer)
	}
	if strings.Contains(footer, "/etc/passwd") {
		t.Errorf("footer leaked command line: %s", footer)
	}
}

func TestConsumeCodexEventsReplacesProgressWithFinalAnswer(t *testing.T) {
	state := &CodexSessionState{Status: "running", ProjectID: "ai-agent"}
	events := make(chan CodexProxyEvent, 3)
	events <- CodexProxyEvent{Type: "chat", Output: "진행 중"}
	events <- CodexProxyEvent{Type: "chat", Output: "최종 답변"}
	events <- CodexProxyEvent{Type: "completed", Output: "최종 답변"}
	close(events)
	consumeCodexEvents(state, events)

	if state.Status != "completed" || state.FinalResponse != "최종 답변" {
		t.Fatalf("completed state = %q, final response = %q", state.Status, state.FinalResponse)
	}
	for _, component := range buildCodexComponents(state) {
		if display, ok := component.(discordgo.TextDisplay); ok && strings.Contains(display.Content, "진행 중") {
			t.Fatalf("completed card still contains progress text: %q", display.Content)
		}
	}
}
