package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

type codexTestTransport func(*http.Request) (*http.Response, error)

func (transport codexTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestCodexStoredSessionRoundTrip(t *testing.T) {
	original := &CodexSessionState{
		SessionID: "codex_proj_test", ProjectID: "test", TurnID: "turn-1",
		UserID: "user", AppID: "app", InteractionToken: "token",
		Prompt: "continue", ChannelID: "channel", MessageID: "message",
		RequestedAt: 120, ProxyStartedAt: 123.5, Status: "running", ApprovalNotified: true,
	}
	data, err := json.Marshal(codexSessionForStorage(original))
	if err != nil {
		t.Fatal(err)
	}
	var stored storedCodexSession
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	restored := stored.restore()
	if restored.SessionID != original.SessionID || restored.TurnID != original.TurnID ||
		restored.MessageID != original.MessageID || restored.ChannelID != original.ChannelID ||
		restored.ProxyStartedAt != original.ProxyStartedAt || restored.RequestedAt != original.RequestedAt || !restored.ApprovalNotified ||
		restored.Interaction == nil || restored.Interaction.Token != original.InteractionToken {
		t.Fatalf("restored Codex binding differs: %+v", restored)
	}
}

func TestCodexRecoveryReplaysProxyAndDisablesCompletedButton(t *testing.T) {
	state := &CodexSessionState{SessionID: "codex_proj_test", ProjectID: "test", TurnID: "turn-1", ProxyStartedAt: 123.5, Status: "running"}
	info := &codexProxySessionInfo{
		TurnID: "turn-1", StartedAt: 123.5, Status: "completed",
		Thinking: []string{"working"}, Chat: []string{"progress"}, FinalResponse: "answer",
		Tools: []string{"git status --short"}, Usage: TokenUsage{TotalTokens: 42},
	}
	applyCodexProxySession(state, info)
	if state.Status != "completed" || state.FinalResponse != "answer" || state.Usage.TotalTokens != 42 ||
		len(state.RawToolNames) != 1 || state.RawToolNames[0] != "shell" {
		t.Fatalf("proxy snapshot not restored: %+v", state)
	}
	var foundDisabledCancel bool
	for _, component := range buildCodexComponents(state) {
		row, ok := component.(discordgo.ActionsRow)
		if !ok {
			continue
		}
		for _, child := range row.Components {
			button, ok := child.(discordgo.Button)
			if ok && strings.HasPrefix(button.CustomID, "codex_cancel:") && button.Disabled {
				foundDisabledCancel = true
			}
		}
	}
	if !foundDisabledCancel {
		t.Fatal("completed Codex card did not disable Cancel")
	}
}

func TestCodexRecoveryRejectsNewerTurn(t *testing.T) {
	state := &CodexSessionState{SessionID: "codex_proj_test", TurnID: "old", ProxyStartedAt: 100, Status: "running"}
	info := &codexProxySessionInfo{TurnID: "new", StartedAt: 200, Status: "running", Chat: []string{"new output"}}
	if codexTurnMatches(state.TurnID, state.ProxyStartedAt, info) {
		t.Fatal("new turn incorrectly matches old Discord card")
	}
	applyCodexProxySession(state, info)
	if state.Status != "error" || len(state.ChatLogs) != 0 || !strings.Contains(state.ErrorMessage, "새 작업") {
		t.Fatalf("new turn was applied to old Discord card: %+v", state)
	}
}

func TestCodexUnboundCardIgnoresPreviousProjectTurn(t *testing.T) {
	state := &CodexSessionState{Prompt: "new request", RequestedAt: 200, Status: "running"}
	old := &codexProxySessionInfo{Prompt: "old request", StartedAt: 100, Status: "completed"}
	if codexStateMatchesProxy(state, old) {
		t.Fatal("previous project turn attached to new Discord card")
	}
	current := &codexProxySessionInfo{Prompt: "new request", StartedAt: 201, Status: "running"}
	if !codexStateMatchesProxy(state, current) {
		t.Fatal("new project turn did not match its Discord card")
	}
}

func TestFetchCodexProxySessionAcceptsListApprovalCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/session" || r.URL.Query().Get("sessionId") != "codex_proj_test" {
			t.Errorf("unexpected proxy request: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sessionId":"codex_proj_test","status":"waiting_approval","pendingApproval":{"command":["git","status"]}}`))
	}))
	defer server.Close()
	previous := codexProxyURL
	codexProxyURL = server.URL
	defer func() { codexProxyURL = previous }()
	info, err := fetchCodexProxySession("codex_proj_test")
	if err != nil {
		t.Fatal(err)
	}
	state := &CodexSessionState{}
	applyCodexProxySession(state, info)
	if state.Status != "waiting_approval" || !strings.Contains(state.PendingApproval, "git") {
		t.Fatalf("approval state not restored: %+v", state)
	}
}

func TestCodexUnexpectedStreamCloseLeavesTurnForRecovery(t *testing.T) {
	state := &CodexSessionState{Status: "running"}
	events := make(chan CodexProxyEvent)
	close(events)
	consumeCodexEvents(state, events)
	if !state.StreamClosed || state.Status != "running" {
		t.Fatalf("closed stream should be recovered from proxy: %+v", state)
	}
}

func TestCodexCompletedCardFallsBackToChannelEditAfterWebhookExpiry(t *testing.T) {
	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatal(err)
	}
	var webhookEdits, channelEdits int
	session.Client = &http.Client{Transport: codexTestTransport(func(request *http.Request) (*http.Response, error) {
		status, body := http.StatusOK, `{"id":"message"}`
		switch {
		case strings.Contains(request.URL.Path, "/webhooks/"):
			webhookEdits++
			status, body = http.StatusUnauthorized, `{"message":"expired token","code":50027}`
		case strings.Contains(request.URL.Path, "/channels/channel/messages/message"):
			channelEdits++
			payload, readErr := io.ReadAll(request.Body)
			if readErr != nil {
				t.Error(readErr)
			}
			if !strings.Contains(string(payload), `"disabled":true`) || !strings.Contains(string(payload), `codex_cancel:codex_proj_test`) {
				t.Errorf("completed card did not disable Cancel: %s", payload)
			}
		default:
			t.Errorf("unexpected Discord request: %s", request.URL.Path)
		}
		return &http.Response{
			StatusCode: status, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(body)), Request: request,
		}, nil
	})}
	state := &CodexSessionState{
		SessionID: "codex_proj_test", ProjectID: "test", Status: "completed",
		AppID: "app", InteractionToken: "expired", ChannelID: "channel", MessageID: "message",
	}
	if err := updateCodexDiscordMessage(session, state); err != nil {
		t.Fatal(err)
	}
	if webhookEdits != 1 || channelEdits != 1 || !state.Finalized {
		t.Fatalf("webhook edits=%d, channel edits=%d, finalized=%v", webhookEdits, channelEdits, state.Finalized)
	}
}
