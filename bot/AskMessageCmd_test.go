package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestBuildAskMessagePrompt(t *testing.T) {
	state := &askMessageState{
		MessageID:  "123456",
		AuthorName: "테스터",
		Username:   "tester",
		Content:    "이 메시지를 요약해 주세요.",
		Attachments: []*discordgo.MessageAttachment{
			{ID: "file-1", Filename: "sample.txt", ContentType: "text/plain", URL: "https://cdn.example/sample.txt"},
		},
		Embeds: []*discordgo.MessageEmbed{
			{Title: "관련 문서", Description: "문서 설명", URL: "https://example.com/docs"},
		},
	}

	prompt := buildAskMessagePrompt(state, "핵심이 뭐야?")
	for _, expected := range []string{
		"<discord_message>", "작성자: 테스터 (@tester)", "메시지 ID: 123456",
		"이 메시지를 요약해 주세요.", "sample.txt (text/plain): https://cdn.example/sample.txt",
		"관련 문서 | 문서 설명 | https://example.com/docs", "<user_question>", "핵심이 뭐야?",
	} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("prompt does not contain %q:\n%s", expected, prompt)
		}
	}
}

func TestAskMessageVisibleTextFromComponentsV2(t *testing.T) {
	message := &discordgo.Message{
		ID:     "v2-message",
		Author: &discordgo.User{Username: "bot"},
		Components: []discordgo.MessageComponent{
			&discordgo.TextDisplay{Content: "### 제목"},
			&discordgo.Container{Components: []discordgo.MessageComponent{
				&discordgo.TextDisplay{Content: "본문 첫 줄"},
				&discordgo.Section{Components: []discordgo.MessageComponent{
					&discordgo.TextDisplay{Content: "본문 둘째 줄"},
				}},
				&discordgo.ActionsRow{Components: []discordgo.MessageComponent{
					&discordgo.Button{Label: "누르기", CustomID: "button"},
				}},
			}},
		},
	}
	want := "### 제목\n본문 첫 줄\n본문 둘째 줄"
	if got := askMessageVisibleText(message); got != want {
		t.Fatalf("visible text = %q, want %q", got, want)
	}
	if got := newAskMessageState("owner", message).Content; got != want {
		t.Fatalf("stored content = %q, want %q", got, want)
	}
	preview := formatAskMessagePreview(message)
	if !strings.Contains(preview, "본문 둘째 줄") || strings.Contains(preview, "누르기") {
		t.Fatalf("incorrect preview: %q", preview)
	}
	prompt := buildAskMessagePrompt(newAskMessageState("owner", message), "요약해줘")
	if !strings.Contains(prompt, want) || strings.Contains(prompt, "누르기") {
		t.Fatalf("incorrect prompt: %q", prompt)
	}
}

func TestAskMessageVisibleTextPrefersPlainContent(t *testing.T) {
	message := &discordgo.Message{
		Content: "사용자가 보낸 일반 메시지",
		Components: []discordgo.MessageComponent{
			discordgo.TextDisplay{Content: "중복 텍스트"},
		},
	}
	if got := askMessageVisibleText(message); got != message.Content {
		t.Fatalf("visible text = %q, want %q", got, message.Content)
	}
}

func TestAskMessageVisibleTextFromResolvedInteraction(t *testing.T) {
	const payload = `{
		"id":"interaction-1","type":2,"data":{"name":"AI한테 질문","type":3,"target_id":"message-1",
		"resolved":{"messages":{"message-1":{"id":"message-1","content":"","components":[
			{"type":10,"content":"제목"},
			{"type":17,"components":[{"type":9,"components":[{"type":10,"content":"카드 본문"}]}]}
		]}}}}
	}`
	var interaction discordgo.InteractionCreate
	if err := json.Unmarshal([]byte(payload), &interaction); err != nil {
		t.Fatal(err)
	}
	data := interaction.ApplicationCommandData()
	message := data.Resolved.Messages[data.TargetID]
	if got, want := askMessageVisibleText(message), "제목\n카드 본문"; got != want {
		t.Fatalf("resolved visible text = %q, want %q", got, want)
	}
}

func TestParseAskMessageIDs(t *testing.T) {
	provider, requestID, ok := parseAskMessageComponentID("aiq_codex:abc123")
	if !ok || provider != "codex" || requestID != "abc123" {
		t.Fatalf("unexpected component parse result: %q %q %v", provider, requestID, ok)
	}
	provider, requestID, ok = parseAskMessageModalID("aiq_modal:antigravity:abc123")
	if !ok || provider != "antigravity" || requestID != "abc123" {
		t.Fatalf("unexpected modal parse result: %q %q %v", provider, requestID, ok)
	}
	if _, _, ok := parseAskMessageComponentID("aiq_unknown:abc123"); ok {
		t.Fatal("unknown provider should be rejected")
	}
}

func TestBuildAskMessageCommandIsUserInstallMessageCommand(t *testing.T) {
	command, err := buildAskMessageCommand()
	if err != nil {
		t.Fatal(err)
	}
	if command.commandType != discordgo.MessageApplicationCommand {
		t.Fatalf("unexpected command type: %v", command.commandType)
	}
	if command.integrationTypes == nil || len(*command.integrationTypes) != 1 || (*command.integrationTypes)[0] != discordgo.ApplicationIntegrationUserInstall {
		t.Fatalf("command must be user-install only: %#v", command.integrationTypes)
	}
}

func TestMakeAskMessageProviderInteraction(t *testing.T) {
	original := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID:        "interaction-1",
		Type:      discordgo.InteractionModalSubmit,
		Token:     "token-1",
		ChannelID: "channel-1",
	}}
	attachments := []*discordgo.MessageAttachment{
		{ID: "a1", Filename: "one.png", URL: "https://cdn.example/one.png"},
		{ID: "a2", Filename: "two.txt", URL: "https://cdn.example/two.txt"},
	}

	synthetic := makeAskMessageProviderInteraction(original, "codex", "질문", attachments)
	if synthetic == original || synthetic.Interaction == original.Interaction {
		t.Fatal("provider interaction must be a copy")
	}
	if synthetic.Type != discordgo.InteractionApplicationCommand {
		t.Fatalf("unexpected interaction type: %v", synthetic.Type)
	}
	data := synthetic.ApplicationCommandData()
	if data.Name != "codex" || len(data.Options) != 1 || data.Options[0].Name != "ask" {
		t.Fatalf("unexpected application command data: %#v", data)
	}
	if len(data.Options[0].Options) != 3 {
		t.Fatalf("expected prompt and two file options, got %d", len(data.Options[0].Options))
	}
	if len(data.Resolved.Attachments) != 2 {
		t.Fatalf("expected two resolved attachments, got %d", len(data.Resolved.Attachments))
	}
	if original.Type != discordgo.InteractionModalSubmit {
		t.Fatal("original interaction was mutated")
	}

	antigravity := makeAskMessageProviderInteraction(original, "antigravity", "질문", attachments)
	agData := antigravity.ApplicationCommandData()
	if len(agData.Resolved.Attachments) != 1 || len(agData.Options[0].Options) != 2 {
		t.Fatal("Antigravity route must forward only the first attachment")
	}
}
