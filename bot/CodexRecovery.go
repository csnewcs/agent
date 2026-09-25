package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	codexRecoveryInterval = 2 * time.Second
	codexStartGrace       = 3 * time.Minute
)

// The proxy owns the turn and its output. Redis only keeps the Discord message
// binding needed to reconnect that turn to its original card after a bot restart.
type storedCodexSession struct {
	SessionID          string  `json:"sessionId"`
	ProjectID          string  `json:"projectId"`
	TurnID             string  `json:"turnId"`
	Model              string  `json:"model"`
	Effort             string  `json:"effort"`
	UserID             string  `json:"userId"`
	AppID              string  `json:"appId"`
	InteractionToken   string  `json:"interactionToken"`
	Prompt             string  `json:"prompt"`
	ChannelID          string  `json:"channelId"`
	MessageID          string  `json:"messageId"`
	RequestedAt        float64 `json:"requestedAt"`
	ProxyStartedAt     float64 `json:"proxyStartedAt"`
	Status             string  `json:"status"`
	ApprovalNotified   bool    `json:"approvalNotified"`
	CompletionNotified bool    `json:"completionNotified"`
}

func codexSessionForStorage(state *CodexSessionState) storedCodexSession {
	state.mu.Lock()
	defer state.mu.Unlock()
	return storedCodexSession{
		SessionID: state.SessionID, ProjectID: state.ProjectID, TurnID: state.TurnID,
		Model: state.Model, Effort: state.Effort, UserID: state.UserID,
		AppID: state.AppID, InteractionToken: state.InteractionToken,
		Prompt: state.Prompt, ChannelID: state.ChannelID, MessageID: state.MessageID,
		RequestedAt:    state.RequestedAt,
		ProxyStartedAt: state.ProxyStartedAt, Status: state.Status,
		ApprovalNotified: state.ApprovalNotified, CompletionNotified: state.CompletionNotified,
	}
}

func (meta storedCodexSession) restore() *CodexSessionState {
	state := &CodexSessionState{
		SessionID: meta.SessionID, ProjectID: meta.ProjectID, TurnID: meta.TurnID,
		Model: meta.Model, Effort: meta.Effort, UserID: meta.UserID,
		AppID: meta.AppID, InteractionToken: meta.InteractionToken,
		Prompt: meta.Prompt, ChannelID: meta.ChannelID, MessageID: meta.MessageID,
		RequestedAt:    meta.RequestedAt,
		ProxyStartedAt: meta.ProxyStartedAt, Status: meta.Status,
		ApprovalNotified: meta.ApprovalNotified, CompletionNotified: meta.CompletionNotified,
	}
	if meta.InteractionToken != "" {
		state.Interaction = &discordgo.Interaction{AppID: meta.AppID, Token: meta.InteractionToken}
	}
	return state
}

func (r *RedisClient) SaveCodexSessionState(state *CodexSessionState) error {
	if r == nil || r.rdb == nil || state == nil {
		return nil
	}
	meta := codexSessionForStorage(state)
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	key := "codex:active:" + meta.SessionID
	if err := r.rdb.Set(ctx, key, data, 7*24*time.Hour).Err(); err != nil {
		return err
	}
	return r.rdb.SAdd(ctx, "codex:active", meta.SessionID).Err()
}

func (r *RedisClient) LoadCodexSessions() ([]*CodexSessionState, error) {
	if r == nil || r.rdb == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ids, err := r.rdb.SMembers(ctx, "codex:active").Result()
	if err != nil {
		return nil, err
	}
	states := make([]*CodexSessionState, 0, len(ids))
	for _, id := range ids {
		data, err := r.rdb.Get(ctx, "codex:active:"+id).Bytes()
		if err != nil {
			continue
		}
		var meta storedCodexSession
		if json.Unmarshal(data, &meta) == nil && meta.SessionID == id {
			states = append(states, meta.restore())
		}
	}
	return states, nil
}

func (r *RedisClient) DeleteCodexSessionState(sessionID string) error {
	if r == nil || r.rdb == nil || sessionID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := r.rdb.Del(ctx, "codex:active:"+sessionID).Err(); err != nil {
		return err
	}
	return r.rdb.SRem(ctx, "codex:active", sessionID).Err()
}

func saveCodexSessionState(state *CodexSessionState) {
	if err := redisClient.SaveCodexSessionState(state); err != nil {
		slog.Warn("Failed to persist Codex Discord binding", "session_id", state.SessionID, "error", err)
	}
}

func codexTerminalStatus(status string) bool {
	return status == "completed" || status == "cancelled" || status == "error"
}

type codexProxySessionInfo struct {
	SessionID       string     `json:"sessionId"`
	TurnID          string     `json:"turnId"`
	Model           string     `json:"model"`
	Effort          string     `json:"effort"`
	Prompt          string     `json:"prompt"`
	Status          string     `json:"status"`
	Tools           []string   `json:"tools"`
	Thinking        []string   `json:"thinking"`
	Chat            []string   `json:"chat"`
	FinalResponse   string     `json:"finalResponse"`
	ErrorMessage    string     `json:"errorMessage"`
	IsRunning       bool       `json:"isRunning"`
	Usage           TokenUsage `json:"usage"`
	StartedAt       float64    `json:"startedAt"`
	UpdatedAt       float64    `json:"updatedAt"`
	PendingApproval *struct {
		Reason  string `json:"reason"`
		Command any    `json:"command"`
		Method  string `json:"method"`
	} `json:"pendingApproval"`
}

func fetchCodexProxySession(sessionID string) (*codexProxySessionInfo, error) {
	endpoint := codexProxyURL + "/api/session?sessionId=" + url.QueryEscape(sessionID)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Codex proxy session returned HTTP %d", resp.StatusCode)
	}
	var info codexProxySessionInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

// A project session can be reused for a later turn. Never apply that newer
// turn's output (or its Cancel button) to an older Discord message.
func applyCodexProxySession(state *CodexSessionState, info *codexProxySessionInfo) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if !codexTurnMatches(state.TurnID, state.ProxyStartedAt, info) {
		state.Status = "error"
		state.ErrorMessage = "같은 프로젝트에서 새 작업이 시작되어 이전 작업의 상태를 복구할 수 없습니다."
		return
	}
	if info.TurnID != "" {
		state.TurnID = info.TurnID
	}
	if info.StartedAt > 0 {
		state.ProxyStartedAt = info.StartedAt
	}
	if info.Model != "" {
		state.Model = info.Model
	}
	if info.Effort != "" {
		state.Effort = info.Effort
	}
	if info.Status == "interrupted" {
		state.Status = "error"
		state.ErrorMessage = "Codex 프록시가 재시작되어 작업이 중단되었습니다."
	} else if info.Status != "" && info.Status != "idle" {
		state.Status = info.Status
	}
	state.ThinkingLogs = append([]string(nil), info.Thinking...)
	state.ChatLogs = append([]string(nil), info.Chat...)
	state.RawToolNames = state.RawToolNames[:0]
	for _, tool := range info.Tools {
		if name := normalizeCodexToolName(tool); name != "" {
			state.RawToolNames = append(state.RawToolNames, name)
		}
	}
	state.FinalResponse = strings.TrimSpace(info.FinalResponse)
	if state.Status == "completed" && state.FinalResponse == "" {
		state.FinalResponse = strings.TrimSpace(strings.Join(state.ChatLogs, ""))
	}
	if info.ErrorMessage != "" {
		state.ErrorMessage = info.ErrorMessage
	}
	state.Usage = info.Usage
	if info.PendingApproval != nil {
		command := ""
		if info.PendingApproval.Command != nil {
			command = fmt.Sprint(info.PendingApproval.Command)
		}
		state.PendingApproval = firstNonEmpty(info.PendingApproval.Reason, command, info.PendingApproval.Method)
	} else {
		state.PendingApproval = ""
	}
}

func codexTurnMatches(turnID string, startedAt float64, info *codexProxySessionInfo) bool {
	if info == nil {
		return false
	}
	return (turnID == "" || info.TurnID == "" || turnID == info.TurnID) &&
		(startedAt == 0 || info.StartedAt == 0 || startedAt == info.StartedAt)
}

// Before /api/chat has returned a turn ID, an older completed project turn may
// still be visible from the proxy. Do not attach it to the new Discord card.
func codexStateMatchesProxy(state *CodexSessionState, info *codexProxySessionInfo) bool {
	state.mu.Lock()
	turnID, startedAt, requestedAt, prompt := state.TurnID, state.ProxyStartedAt, state.RequestedAt, state.Prompt
	state.mu.Unlock()
	if turnID == "" && startedAt == 0 && requestedAt > 0 {
		return info != nil && info.StartedAt >= requestedAt-1 && info.Prompt == prompt
	}
	return codexTurnMatches(turnID, startedAt, info)
}

func bindCodexProxyTurn(state *CodexSessionState, info *codexProxySessionInfo) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.TurnID = info.TurnID
	state.ProxyStartedAt = info.StartedAt
}

func validateCodexActionTurn(s *discordgo.Session, ic *discordgo.InteractionCreate, state *CodexSessionState) bool {
	info, err := fetchCodexProxySession(state.SessionID)
	if err != nil {
		_ = RespondComponentsV2(s, ic, SimpleErrorCard("Codex 작업 상태를 확인할 수 없습니다: "+err.Error()), true)
		return false
	}
	if !codexStateMatchesProxy(state, info) {
		_ = RespondComponentsV2(s, ic, SimpleErrorCard("이전 Codex 작업의 버튼입니다."), true)
		return false
	}
	if codexTerminalStatus(info.Status) || info.Status == "interrupted" {
		applyCodexProxySession(state, info)
		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate})
		_ = updateCodexDiscordMessage(s, state)
		return false
	}
	return true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func InitCodexBotStartup() {
	states, err := redisClient.LoadCodexSessions()
	if err != nil {
		slog.Warn("Failed to load Codex Discord bindings", "error", err)
		return
	}
	for _, state := range states {
		registerCodexSession(state)
	}
	slog.Info("Loaded Codex sessions on startup", "count", len(states))
}

func StartCodexRecoveryWorker(s *discordgo.Session) {
	codexSessionsMu.RLock()
	var pending []*CodexSessionState
	for _, state := range codexSessions {
		state.mu.Lock()
		if !state.Finalized && state.CancelFunc == nil {
			pending = append(pending, state)
		}
		state.mu.Unlock()
	}
	codexSessionsMu.RUnlock()
	for _, state := range pending {
		startCodexRecoveryWorker(s, state)
	}
	if len(pending) == 0 {
		slog.Info("No Codex sessions to recover on startup")
	}
}

func startCodexRecoveryWorker(s *discordgo.Session, state *CodexSessionState) {
	state.mu.Lock()
	if state.Recovering || state.Finalized {
		state.mu.Unlock()
		return
	}
	state.Recovering = true
	state.mu.Unlock()
	go recoverCodexSession(s, state)
}

func recoverCodexSession(s *discordgo.Session, state *CodexSessionState) {
	defer func() {
		state.mu.Lock()
		state.Recovering = false
		state.mu.Unlock()
	}()
	ticker := time.NewTicker(codexRecoveryInterval)
	defer ticker.Stop()
	lastProxySuccess := time.Now()
	var firstEditFailure time.Time
	lastFingerprint := ""
	for {
		info, err := fetchCodexProxySession(state.SessionID)
		if err != nil {
			if time.Since(lastProxySuccess) > 15*time.Minute {
				state.mu.Lock()
				state.Status = "error"
				state.ErrorMessage = "재시작 후 Codex 프록시 상태를 15분 동안 확인하지 못했습니다."
				state.mu.Unlock()
				_ = updateCodexDiscordMessage(s, state)
				return
			}
		} else {
			lastProxySuccess = time.Now()
			if !codexStateMatchesProxy(state, info) {
				state.mu.Lock()
				unbound := state.TurnID == "" && state.ProxyStartedAt == 0
				requestedAt := state.RequestedAt
				state.mu.Unlock()
				if unbound && time.Since(time.Unix(0, int64(requestedAt*1e9))) < codexStartGrace {
					<-ticker.C
					continue
				}
				if unbound {
					state.mu.Lock()
					state.Status = "error"
					state.ErrorMessage = "Codex 작업이 시작되지 않아 복구할 수 없습니다."
					state.mu.Unlock()
					_ = updateCodexDiscordMessage(s, state)
					return
				}
			}
			applyCodexProxySession(state, info)
			fingerprint := codexStateFingerprint(state)
			state.mu.Lock()
			status := state.Status
			state.mu.Unlock()
			if fingerprint != lastFingerprint || codexTerminalStatus(status) {
				if err := updateCodexDiscordMessage(s, state); err != nil {
					if firstEditFailure.IsZero() {
						firstEditFailure = time.Now()
					}
					if time.Since(firstEditFailure) > 15*time.Minute {
						slog.Error("Codex recovery cannot edit original message", "session_id", state.SessionID, "error", err)
						return
					}
				} else {
					firstEditFailure = time.Time{}
					lastFingerprint = fingerprint
					if status == "waiting_approval" {
						state.mu.Lock()
						shouldNotify := !state.ApprovalNotified
						state.ApprovalNotified = true
						state.mu.Unlock()
						if shouldNotify {
							saveCodexSessionState(state)
							notifyCodexUser(s, state, "Codex 작업에 승인이 필요합니다.")
						}
					}
					if codexTerminalStatus(status) {
						if status == "completed" || status == "error" {
							state.mu.Lock()
							shouldNotify := !state.CompletionNotified
							state.CompletionNotified = true
							state.mu.Unlock()
							if shouldNotify {
								message := "Codex 작업이 완료되었습니다."
								if status == "error" {
									message = "Codex 작업 중 오류가 발생했습니다."
								}
								notifyCodexUser(s, state, message)
							}
						}
						return
					}
				}
			}
		}
		<-ticker.C
	}
}

func codexStateFingerprint(state *CodexSessionState) string {
	state.mu.Lock()
	defer state.mu.Unlock()
	return fmt.Sprintf("%s:%d:%d:%d:%d:%d:%d:%s", state.Status, len(state.ThinkingLogs), len(state.ChatLogs),
		len(state.RawToolNames), len(state.FinalResponse), len(state.ErrorMessage), state.Usage.TotalTokens, state.PendingApproval)
}

func updateCodexDiscordMessage(s *discordgo.Session, state *CodexSessionState) error {
	components := buildCodexComponents(state)
	state.mu.Lock()
	appID, token := state.AppID, state.InteractionToken
	channelID, messageID := state.ChannelID, state.MessageID
	status := state.Status
	state.mu.Unlock()
	if appID == "" && s.State != nil && s.State.User != nil {
		appID = s.State.User.ID
	}
	var editErr error = fmt.Errorf("no Codex message edit method available")
	if appID != "" && token != "" {
		uri := discordgo.EndpointWebhookMessage(appID, token, "@original")
		data := WebhookEditComponentsV2{Components: &components, Flags: discordgo.MessageFlagsIsComponentsV2}
		response, err := s.RequestWithBucketID("PATCH", uri, data, discordgo.EndpointWebhookToken("", ""))
		if err == nil {
			editErr = nil
			var msg discordgo.Message
			if json.Unmarshal(response, &msg) == nil && msg.ID != "" && msg.ID != messageID {
				state.mu.Lock()
				state.MessageID = msg.ID
				state.mu.Unlock()
				saveCodexSessionState(state)
			}
		} else {
			editErr = err
		}
	}
	if editErr != nil && channelID != "" && messageID != "" {
		_, editErr = s.ChannelMessageEditComplex(&discordgo.MessageEdit{
			Channel: channelID, ID: messageID, Flags: discordgo.MessageFlagsIsComponentsV2,
			Components: &components,
		})
	}
	if editErr == nil && codexTerminalStatus(status) {
		state.mu.Lock()
		state.Finalized = true
		state.mu.Unlock()
		if err := redisClient.DeleteCodexSessionState(state.SessionID); err != nil {
			slog.Warn("Failed to clear finalized Codex binding", "session_id", state.SessionID, "error", err)
		}
	}
	return editErr
}
