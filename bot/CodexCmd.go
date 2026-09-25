package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

var codexProxyURL = strings.TrimRight(getEnv("CODEX_PROXY_URL", "http://127.0.0.1:8091"), "/")

func InitCodexProxy() {
	slog.Info("Codex HTTP REST Proxy initialized", "url", codexProxyURL)
}

type CodexProxyEvent struct {
	SessionID      string `json:"sessionId"`
	Type           string `json:"type"`
	Tool           string `json:"tool"`
	Output         string `json:"output"`
	RequestID      any    `json:"requestId"`
	ApprovalMethod string `json:"approvalMethod"`
}

type CodexSessionState struct {
	mu                 sync.Mutex
	SessionID          string
	ProjectID          string
	TurnID             string
	Model              string
	Effort             string
	UserID             string
	AppID              string
	InteractionToken   string
	Prompt             string
	ChannelID          string
	MessageID          string
	RequestedAt        float64
	ProxyStartedAt     float64
	Finalized          bool
	Recovering         bool
	StreamClosed       bool
	Status             string
	ThinkingLogs       []string
	ChatLogs           []string
	FinalResponse      string
	RawToolNames       []string
	ErrorMessage       string
	Usage              TokenUsage
	PendingApproval    string
	ApprovalNotified   bool
	CompletionNotified bool
	CancelFunc         func()
	Interaction        *discordgo.Interaction
}

var (
	codexSessionsMu    sync.RWMutex
	codexSessions      = make(map[string]*CodexSessionState)
	codexPreferencesMu sync.RWMutex
	codexPreferences   = make(map[string]CodexPreferences)
)

type CodexPreferences struct {
	ProjectID string `json:"projectId"`
	Model     string `json:"model"`
	Effort    string `json:"effort"`
}

func resolveCodexPreferences(previous CodexPreferences, project, model, effort *string) CodexPreferences {
	if project != nil && strings.TrimSpace(*project) != "" {
		previous.ProjectID = strings.TrimSpace(*project)
	}
	if model != nil {
		previous.Model = strings.TrimSpace(*model)
		if previous.Model == "default" {
			previous.Model = ""
		}
	}
	if effort != nil {
		previous.Effort = strings.TrimSpace(*effort)
		if previous.Effort == "default" {
			previous.Effort = ""
		}
	}
	if previous.ProjectID == "" {
		previous.ProjectID = "ai-agent"
	}
	return previous
}

func loadCodexPreferences(userID string) CodexPreferences {
	codexPreferencesMu.RLock()
	prefs, ok := codexPreferences[userID]
	codexPreferencesMu.RUnlock()
	if ok {
		return prefs
	}
	if stored, found := redisClient.GetCodexPreferences(userID); found {
		prefs = stored
	} else {
		// Preserve the existing project/model/effort when upgrading from a bot
		// version that did not store per-user selections.
		prefs = latestCodexSessionPreferences()
	}
	if prefs.ProjectID != "" {
		codexPreferencesMu.Lock()
		codexPreferences[userID] = prefs
		codexPreferencesMu.Unlock()
	}
	return prefs
}

func saveCodexPreferences(userID string, prefs CodexPreferences) {
	codexPreferencesMu.Lock()
	codexPreferences[userID] = prefs
	codexPreferencesMu.Unlock()
	if err := redisClient.SetCodexPreferences(userID, prefs); err != nil {
		slog.Warn("Failed to persist Codex preferences", "error", err)
	}
}

func latestCodexSessionPreferences() CodexPreferences {
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(codexProxyURL + "/api/sessions")
	if err != nil {
		return CodexPreferences{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return CodexPreferences{}
	}
	var data struct {
		Sessions []struct {
			SessionID string  `json:"sessionId"`
			ProjectID string  `json:"projectId"`
			Model     string  `json:"model"`
			Effort    string  `json:"effort"`
			UpdatedAt float64 `json:"updatedAt"`
		} `json:"sessions"`
	}
	if json.NewDecoder(resp.Body).Decode(&data) != nil {
		return CodexPreferences{}
	}
	var latest float64
	var prefs CodexPreferences
	for _, session := range data.Sessions {
		if strings.HasPrefix(session.SessionID, "codex_proj_") && session.UpdatedAt >= latest {
			latest = session.UpdatedAt
			prefs = CodexPreferences{ProjectID: session.ProjectID, Model: session.Model, Effort: session.Effort}
		}
	}
	return prefs
}

func registerCodexSession(state *CodexSessionState) {
	codexSessionsMu.Lock()
	defer codexSessionsMu.Unlock()
	codexSessions[state.SessionID] = state
}

func getCodexSession(sessionID string) *CodexSessionState {
	codexSessionsMu.RLock()
	defer codexSessionsMu.RUnlock()
	return codexSessions[sessionID]
}

func buildCodexCommand() (BotCommand, error) {
	modelChoices := []*discordgo.ApplicationCommandOptionChoice{
		{Name: "Codex 기본 모델", Value: "default"},
		{Name: "GPT-6 Astra", Value: "gpt-6-astra"},
		{Name: "GPT-6 Sol", Value: "gpt-6-sol"},
		{Name: "GPT-6 Luna", Value: "gpt-6-luna"},
		{Name: "GPT-5.6 Sol", Value: "gpt-5.6-sol"},
		{Name: "GPT-5.6 Terra", Value: "gpt-5.6-terra"},
		{Name: "GPT-5.6 Luna", Value: "gpt-5.6-luna"},
		{Name: "GPT-5.5", Value: "gpt-5.5"},
	}
	effortChoices := []*discordgo.ApplicationCommandOptionChoice{
		{Name: "모델 기본값", Value: "default"},
		{Name: "Low", Value: "low"},
		{Name: "Medium", Value: "medium"},
		{Name: "High", Value: "high"},
		{Name: "XHigh", Value: "xhigh"},
		{Name: "Max", Value: "max"},
		{Name: "Ultra", Value: "ultra"},
	}
	fileOptions := []*discordgo.ApplicationCommandOption{}
	for i := 1; i <= 5; i++ {
		name := "file"
		if i > 1 {
			name = fmt.Sprintf("file%d", i)
		}
		fileOptions = append(fileOptions, &discordgo.ApplicationCommandOption{
			Type: discordgo.ApplicationCommandOptionAttachment, Name: name, Description: ".", Required: false,
		})
	}
	askOptions := []*discordgo.ApplicationCommandOption{
		{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: ".", Required: true},
		{Type: discordgo.ApplicationCommandOptionString, Name: "project", Description: ".", Required: false, Autocomplete: true},
		{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: ".", Required: false, Choices: modelChoices},
		{Type: discordgo.ApplicationCommandOptionString, Name: "effort", Description: ".", Required: false, Choices: effortChoices},
	}
	askOptions = append(askOptions, fileOptions...)
	projectOption := []*discordgo.ApplicationCommandOption{
		{Type: discordgo.ApplicationCommandOptionString, Name: "project", Description: ".", Required: false, Autocomplete: true},
	}
	return NewBotCommandBuilder("codex").
		WithDescription(".").
		AddArg(&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "ask", Description: ".", Options: askOptions}).
		AddArg(&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "clear", Description: ".", Options: projectOption}).
		AddArg(&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "compact", Description: ".", Options: projectOption}).
		WithFunction(handleCodexCommand).
		Build()
}

func handleCodexCommand(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	err := s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsIsComponentsV2},
	})
	if err != nil {
		slog.Error("Failed to defer /codex", "error", err)
		return
	}
	go runCodexCommand(s, ic)
}

func runCodexCommand(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	options := ic.ApplicationCommandData().Options
	if len(options) == 0 {
		_, _ = editInteractionComponentsV2(s, ic, SimpleErrorCard("Codex 하위 명령을 선택해 주세요."))
		return
	}
	sub := options[0]
	prompt := ""
	var projectOption, modelOption, effortOption *string
	var files []ProxyFileAttachment
	for _, opt := range sub.Options {
		switch opt.Name {
		case "prompt":
			prompt = strings.TrimSpace(opt.StringValue())
		case "project":
			value := opt.StringValue()
			projectOption = &value
		case "model":
			value := opt.StringValue()
			modelOption = &value
		case "effort":
			value := opt.StringValue()
			effortOption = &value
		default:
			if strings.HasPrefix(opt.Name, "file") && ic.ApplicationCommandData().Resolved != nil {
				attachment := ic.ApplicationCommandData().Resolved.Attachments[opt.Value.(string)]
				if attachment != nil {
					files = append(files, ProxyFileAttachment{Name: attachment.Filename, URL: attachment.URL, Size: attachment.Size})
				}
			}
		}
	}
	userID := getUserID(ic)
	prefs := resolveCodexPreferences(loadCodexPreferences(userID), projectOption, modelOption, effortOption)
	projectID, model, effort := prefs.ProjectID, prefs.Model, prefs.Effort
	sessionID := "codex_proj_" + sanitizeCodexID(projectID)
	switch sub.Name {
	case "clear":
		if err := postCodexAction("/api/clear", map[string]any{"sessionId": sessionID}); err != nil {
			_, _ = editInteractionComponentsV2(s, ic, SimpleErrorCard("Codex 세션 초기화 실패: "+err.Error()))
			return
		}
		codexSessionsMu.Lock()
		delete(codexSessions, sessionID)
		codexSessionsMu.Unlock()
		if err := redisClient.DeleteCodexSessionState(sessionID); err != nil {
			slog.Warn("Failed to clear Codex Discord binding", "session_id", sessionID, "error", err)
		}
		_, _ = editInteractionComponentsV2(s, ic, SimpleComponentsCard("Codex 세션 초기화", fmt.Sprintf("`%s` 프로젝트의 대화가 삭제되었습니다.", projectID), "Codex App Server"))
		return
	case "compact":
		if err := postCodexAction("/api/compact", map[string]any{"sessionId": sessionID, "projectId": projectID}); err != nil {
			_, _ = editInteractionComponentsV2(s, ic, SimpleErrorCard("Codex 컨텍스트 압축 실패: "+err.Error()))
			return
		}
		_, _ = editInteractionComponentsV2(s, ic, SimpleComponentsCard("Codex 컨텍스트 압축", "대화 컨텍스트 압축을 시작했습니다.", projectID))
		return
	case "ask":
		if prompt == "" {
			_, _ = editInteractionComponentsV2(s, ic, SimpleErrorCard("프롬프트를 입력해 주세요."))
			return
		}
	default:
		_, _ = editInteractionComponentsV2(s, ic, SimpleErrorCard("지원하지 않는 Codex 명령입니다."))
		return
	}
	if info, err := fetchCodexProxySession(sessionID); err == nil && info.IsRunning {
		_, _ = editInteractionComponentsV2(s, ic, SimpleErrorCard("이 프로젝트에서 Codex 작업이 이미 진행 중입니다. 기존 작업을 완료하거나 중단한 뒤 다시 시도해 주세요."))
		return
	}

	state := &CodexSessionState{
		SessionID: sessionID, ProjectID: projectID, Model: model, Effort: effort,
		UserID: userID, AppID: ic.Interaction.AppID, InteractionToken: ic.Interaction.Token,
		Prompt: prompt, ChannelID: ic.ChannelID,
		RequestedAt: float64(time.Now().UnixNano()) / 1e9,
		Status:      "running", Interaction: ic.Interaction,
	}
	registerCodexSession(state)
	if msg, editErr := editInteractionComponentsV2(s, ic, buildCodexComponents(state)); editErr != nil {
		slog.Warn("Failed initial Codex interaction edit", "session_id", sessionID, "error", editErr)
	} else if msg != nil {
		state.mu.Lock()
		state.MessageID = msg.ID
		state.mu.Unlock()
	}
	saveCodexSessionState(state)

	events, cancel, err := sendCodexChatStream(sessionID, projectID, prompt, model, effort, files)
	if err != nil {
		if info, fetchErr := fetchCodexProxySession(sessionID); fetchErr == nil && info.IsRunning {
			applyCodexProxySession(state, info)
			saveCodexSessionState(state)
			startCodexRecoveryWorker(s, state)
			return
		}
		state.mu.Lock()
		state.Status = "error"
		state.ErrorMessage = err.Error()
		state.mu.Unlock()
		if updateErr := updateCodexDiscordMessage(s, state); updateErr != nil {
			slog.Error("Failed to show Codex proxy error", "session_id", sessionID, "error", updateErr)
		}
		return
	}
	if info, fetchErr := fetchCodexProxySession(sessionID); fetchErr == nil {
		bindCodexProxyTurn(state, info)
		saveCodexSessionState(state)
	}
	saveCodexPreferences(userID, prefs)
	state.mu.Lock()
	state.CancelFunc = cancel
	state.mu.Unlock()
	go consumeCodexEvents(state, events)
	runCodexUpdateLoop(s, state)
}

func sanitizeCodexID(value string) string {
	var out strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			out.WriteRune(r)
		}
	}
	if out.Len() == 0 {
		return "ai-agent"
	}
	return out.String()
}

func sendCodexChatStream(sessionID, projectID, prompt, model, effort string, files []ProxyFileAttachment) (<-chan CodexProxyEvent, func(), error) {
	payload := map[string]any{"sessionId": sessionID, "projectId": projectID, "prompt": prompt, "model": model, "effort": effort, "files": files}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, codexProxyURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		message, _ := io.ReadAll(resp.Body)
		return nil, nil, fmt.Errorf("Codex proxy returned %d: %s", resp.StatusCode, strings.TrimSpace(string(message)))
	}
	out := make(chan CodexProxyEvent, 128)
	cancel := func() {
		_ = resp.Body.Close()
	}
	go func() {
		defer close(out)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		buffer := make([]byte, 64*1024)
		scanner.Buffer(buffer, 2*1024*1024)
		for scanner.Scan() {
			var event CodexProxyEvent
			if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Type != "ping" {
				out <- event
			}
		}
	}()
	return out, cancel, nil
}

func consumeCodexEvents(state *CodexSessionState, events <-chan CodexProxyEvent) {
	for event := range events {
		state.mu.Lock()
		switch event.Type {
		case "thinking":
			state.ThinkingLogs = append(state.ThinkingLogs, event.Output)
		case "chat":
			state.ChatLogs = append(state.ChatLogs, event.Output)
		case "tool":
			if event.Tool != "" {
				toolName := normalizeCodexToolName(event.Tool)
				if toolName != "" {
					state.RawToolNames = append(state.RawToolNames, toolName)
				}
			}
		case "permission_request":
			state.Status = "waiting_approval"
			state.PendingApproval = event.Output
			state.ApprovalNotified = false
		case "usage":
			_ = json.Unmarshal([]byte(event.Output), &state.Usage)
		case "completed":
			state.Status = "completed"
			state.FinalResponse = strings.TrimSpace(event.Output)
			if state.FinalResponse == "" {
				state.FinalResponse = strings.TrimSpace(strings.Join(state.ChatLogs, ""))
			}
		case "cancelled":
			state.Status = "cancelled"
		case "fatal_error":
			state.Status = "error"
			state.ErrorMessage = event.Output
		case "error":
			state.ErrorMessage = event.Output
		}
		state.mu.Unlock()
	}
	state.mu.Lock()
	state.StreamClosed = true
	state.CancelFunc = nil
	state.mu.Unlock()
}

func runCodexUpdateLoop(s *discordgo.Session, state *CodexSessionState) {
	ticker := time.NewTicker(1200 * time.Millisecond)
	defer ticker.Stop()
	last := ""
	for range ticker.C {
		state.mu.Lock()
		fingerprint := fmt.Sprintf("%s:%d:%d:%d:%d:%s", state.Status, len(state.ThinkingLogs), len(state.ChatLogs), len(state.RawToolNames), state.Usage.TotalTokens, state.PendingApproval)
		status := state.Status
		streamClosed := state.StreamClosed
		needsApprovalNotice := status == "waiting_approval" && !state.ApprovalNotified
		needsCompletionNotice := (status == "completed" || status == "error") && !state.CompletionNotified
		if needsApprovalNotice {
			state.ApprovalNotified = true
		}
		if needsCompletionNotice {
			state.CompletionNotified = true
		}
		state.mu.Unlock()
		if streamClosed && !codexTerminalStatus(status) {
			startCodexRecoveryWorker(s, state)
			return
		}
		if fingerprint != last || status == "completed" || status == "cancelled" || status == "error" {
			if err := updateCodexDiscordMessage(s, state); err != nil {
				slog.Error("Failed to update Codex Discord message", "session_id", state.SessionID, "error", err)
				if codexTerminalStatus(status) {
					startCodexRecoveryWorker(s, state)
				}
			} else {
				last = fingerprint
			}
		}
		if needsApprovalNotice {
			notifyCodexUser(s, state, "Codex 작업에 승인이 필요합니다.")
		}
		if needsCompletionNotice {
			message := "Codex 작업이 완료되었습니다."
			if status == "error" {
				message = "Codex 작업 중 오류가 발생했습니다."
			}
			notifyCodexUser(s, state, message)
		}
		if status == "completed" || status == "cancelled" || status == "error" {
			return
		}
	}
}

// notifyCodexUser sends a single explicit alert for a state transition. It uses
// the interaction webhook first, then falls back to a direct message if the
// interaction token has expired during a long-running task.
func notifyCodexUser(s *discordgo.Session, state *CodexSessionState, message string) {
	state.mu.Lock()
	userID := state.UserID
	interaction := state.Interaction
	state.mu.Unlock()
	if userID == "" {
		return
	}

	content := fmt.Sprintf("<@%s> %s", userID, message)
	allowedMentions := &discordgo.MessageAllowedMentions{Users: []string{userID}}
	if interaction != nil {
		if _, err := s.FollowupMessageCreate(interaction, true, &discordgo.WebhookParams{
			Content:         content,
			AllowedMentions: allowedMentions,
		}); err == nil {
			return
		} else {
			slog.Warn("Failed to send Codex interaction notification; trying DM", "session_id", state.SessionID, "error", err)
		}
	}

	dm, err := s.UserChannelCreate(userID)
	if err != nil {
		slog.Warn("Failed to create DM channel for Codex notification", "session_id", state.SessionID, "error", err)
		return
	}
	if _, err := s.ChannelMessageSend(dm.ID, message); err != nil {
		slog.Warn("Failed to send Codex notification DM", "session_id", state.SessionID, "error", err)
	}
}

func buildCodexComponents(state *CodexSessionState) []discordgo.MessageComponent {
	state.mu.Lock()
	defer state.mu.Unlock()
	title := "Codex Agent"
	switch state.Status {
	case "running":
		title += " (작업 중...)"
	case "waiting_approval":
		title += " (승인 대기 중)"
	case "error":
		title += " (오류 발생)"
	case "cancelled":
		title += " (작업 중단됨)"
	}
	body := "**Codex가 작업을 준비하고 있습니다...**"
	if state.FinalResponse != "" {
		parts := splitMarkdownContent(FormatDiscordMarkdown(state.FinalResponse), 1800)
		if len(parts) > 0 {
			body = parts[0]
		}
	} else if len(state.ChatLogs) > 0 {
		parts := splitMarkdownContent(FormatDiscordMarkdown(strings.Join(state.ChatLogs, "")), 1800)
		if len(parts) > 0 {
			body = parts[0]
		}
	} else if len(state.ThinkingLogs) > 0 {
		body = FormatThinkingMarkdown(strings.Join(state.ThinkingLogs, ""), 500)
	} else if len(state.RawToolNames) > 0 && state.Status == "running" {
		body = fmt.Sprintf("**Codex 작업 수행 중...** (도구 %d회 실행 완료)", len(state.RawToolNames))
	}
	if state.Status == "waiting_approval" {
		body += "\n\n**승인이 필요한 작업**\n```\n" + truncateRunes(state.PendingApproval, 700) + "\n```"
	} else if state.Status == "error" {
		body += "\n\n**오류**: `" + truncateRunes(state.ErrorMessage, 500) + "`"
	} else if state.Status == "cancelled" {
		body += "\n\n**사용자 요청으로 작업이 중단되었습니다.**"
	}
	model := state.Model
	if model == "" {
		model = "Codex 기본 모델"
	}
	if state.Effort != "" {
		model += " / " + state.Effort
	}
	footerTools := formatCompressedTools(state.RawToolNames)
	footer := fmt.Sprintf("%s • %s", state.ProjectID, model)
	if footerTools != "" {
		footer = fmt.Sprintf("%s | %s", footerTools, footer)
	}
	if state.Usage.TotalTokens > 0 {
		footer += fmt.Sprintf("\n사용 토큰: Total %s (Input %s | Output %s | Thinking %s | Cache %s)",
			formatNumberWithCommas(state.Usage.TotalTokens), formatNumberWithCommas(state.Usage.InputTokens),
			formatNumberWithCommas(state.Usage.OutputTokens), formatNumberWithCommas(state.Usage.ThinkingTokens),
			formatNumberWithCommas(state.Usage.CacheReadTokens))
	}
	builder := NewComponentsBuilder().WithTitle(title).WithSubTitle(state.Prompt).WithBody(body).WithFooter(footer)
	if state.Status == "waiting_approval" {
		builder.WithButtons(
			discordgo.Button{Label: "이번만 승인", Style: discordgo.SuccessButton, CustomID: "codex_approve:" + state.SessionID},
			discordgo.Button{Label: "세션 동안 승인", Style: discordgo.PrimaryButton, CustomID: "codex_approve_session:" + state.SessionID},
			discordgo.Button{Label: "거절", Style: discordgo.SecondaryButton, CustomID: "codex_decline:" + state.SessionID},
			discordgo.Button{Label: "중단", Style: discordgo.DangerButton, CustomID: "codex_cancel:" + state.SessionID},
		)
	} else {
		builder.WithButtons(discordgo.Button{Label: "중단 (Cancel)", Style: discordgo.DangerButton, CustomID: "codex_cancel:" + state.SessionID, Disabled: state.Status != "running"})
	}
	return builder.Build()
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}

func HandleCodexComponent(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	parts := strings.SplitN(ic.MessageComponentData().CustomID, ":", 2)
	if len(parts) != 2 {
		return
	}
	state := getCodexSession(parts[1])
	if state == nil {
		_ = RespondComponentsV2(s, ic, SimpleErrorCard("활성 Codex 세션을 찾을 수 없습니다."), true)
		return
	}
	state.mu.Lock()
	owner := state.UserID
	messageID := state.MessageID
	state.mu.Unlock()
	if ic.Message != nil && messageID != "" && ic.Message.ID != messageID {
		_ = RespondComponentsV2(s, ic, SimpleErrorCard("이전 Codex 작업의 버튼입니다."), true)
		return
	}
	if owner != "" && !isInteractionOwnerOrAdmin(ic, owner) {
		_ = RespondComponentsV2(s, ic, SimpleErrorCard("명령 실행자 또는 관리자만 조작할 수 있습니다."), true)
		return
	}
	decision := ""
	switch parts[0] {
	case "codex_approve":
		decision = "accept"
	case "codex_approve_session":
		decision = "acceptForSession"
	case "codex_decline":
		decision = "decline"
	case "codex_cancel":
		state.mu.Lock()
		cancel := state.CancelFunc
		status := state.Status
		state.mu.Unlock()
		if codexTerminalStatus(status) {
			_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate})
			_ = updateCodexDiscordMessage(s, state)
			return
		}
		if !validateCodexActionTurn(s, ic, state) {
			return
		}
		if err := postCodexAction("/api/cancel", map[string]any{"sessionId": state.SessionID}); err != nil {
			_ = RespondComponentsV2(s, ic, SimpleErrorCard("Codex 작업 중단 실패: "+err.Error()), true)
			return
		}
		if cancel != nil {
			cancel()
		}
		state.mu.Lock()
		state.Status = "cancelled"
		state.mu.Unlock()
		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate})
		if err := updateCodexDiscordMessage(s, state); err != nil {
			slog.Warn("Failed to disable cancelled Codex button", "session_id", state.SessionID, "error", err)
		}
		return
	}
	if decision != "" {
		if !validateCodexActionTurn(s, ic, state) {
			return
		}
		err := postCodexAction("/api/approval", map[string]any{"sessionId": state.SessionID, "decision": decision})
		if err != nil {
			_ = RespondComponentsV2(s, ic, SimpleErrorCard("승인 처리 실패: "+err.Error()), true)
			return
		}
		state.mu.Lock()
		state.Status = "running"
		state.PendingApproval = ""
		state.mu.Unlock()
		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate})
	}
}

func postCodexAction(path string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Post(codexProxyURL+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(message)))
	}
	return nil
}

func sendCodexProjectAutocomplete(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(codexProxyURL + "/api/projects")
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var data struct {
		Projects []string `json:"projects"`
	}
	if json.NewDecoder(resp.Body).Decode(&data) != nil {
		return
	}
	choices := make([]*discordgo.ApplicationCommandOptionChoice, 0, len(data.Projects))
	for _, project := range data.Projects {
		if len(choices) >= 25 {
			break
		}
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{Name: project, Value: project})
	}
	_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionApplicationCommandAutocompleteResult, Data: &discordgo.InteractionResponseData{Choices: choices}})
}

func normalizeCodexToolName(raw string) string {
	tool := strings.TrimSpace(raw)
	if tool == "" {
		return ""
	}
	switch strings.ToLower(tool) {
	case "shell", "bash", "sh", "exec", "execute", "run_command", "commandexecution":
		return "shell"
	case "apply_patch", "filechange", "file_change":
		return "apply_patch"
	}
	if strings.ContainsAny(tool, " \t\r\n|&;<>()$`\\") || strings.HasPrefix(tool, "/") || strings.HasPrefix(tool, "./") {
		return "shell"
	}
	isIdent := true
	for _, r := range tool {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
			isIdent = false
			break
		}
	}
	if !isIdent {
		return "shell"
	}
	switch strings.ToLower(tool) {
	case "ls", "cat", "grep", "git", "go", "npm", "node", "python", "python3", "docker", "cd", "mkdir", "rm", "cp", "mv", "touch", "echo", "sed", "awk", "find", "pkill", "kill":
		return "shell"
	}
	return tool
}
