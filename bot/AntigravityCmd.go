package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

var (
	lastSessionMu       sync.RWMutex
	globalLastSessionID string
	globalLastProjectID string
)

type WebhookEditComponentsV2 struct {
	Content    *string                       `json:"content,omitempty"`
	Components *[]discordgo.MessageComponent `json:"components,omitempty"`
	Embeds     *[]*discordgo.MessageEmbed    `json:"embeds,omitempty"`
	Flags      discordgo.MessageFlags        `json:"flags"`
}

func editInteractionComponentsV2(s *discordgo.Session, ic *discordgo.InteractionCreate, components []discordgo.MessageComponent) (*discordgo.Message, error) {
	if ic == nil || ic.Interaction == nil {
		return nil, fmt.Errorf("nil interaction")
	}
	appID := ic.Interaction.AppID
	if appID == "" && s.State != nil && s.State.User != nil {
		appID = s.State.User.ID
	}
	token := ic.Interaction.Token
	uri := discordgo.EndpointWebhookMessage(appID, token, "@original")

	data := WebhookEditComponentsV2{
		Components: &components,
		Flags:      discordgo.MessageFlagsIsComponentsV2,
	}

	response, err := s.RequestWithBucketID("PATCH", uri, data, discordgo.EndpointWebhookToken("", ""))
	if err != nil {
		return nil, err
	}

	var msg *discordgo.Message
	err = json.Unmarshal(response, &msg)
	return msg, err
}

func SetLastAntigravitySessionID(sessionID string) {
	lastSessionMu.Lock()
	globalLastSessionID = sessionID
	lastSessionMu.Unlock()

	if redisClient != nil {
		_ = redisClient.SetLastAntigravitySessionID(sessionID)
	}
}

func GetLastAntigravitySessionID() string {
	lastSessionMu.RLock()
	id := globalLastSessionID
	lastSessionMu.RUnlock()

	if id != "" {
		return id
	}

	if redisClient != nil {
		id = redisClient.GetLastAntigravitySessionID()
		if id != "" {
			lastSessionMu.Lock()
			globalLastSessionID = id
			lastSessionMu.Unlock()
			return id
		}
	}
	return ""
}

func SetLastAntigravityProjectID(projectID string) {
	if projectID == "" {
		return
	}
	lastSessionMu.Lock()
	globalLastProjectID = projectID
	lastSessionMu.Unlock()

	if redisClient != nil {
		_ = redisClient.SetLastAntigravityProjectID(projectID)
	}
}

func GetLastAntigravityProjectID() string {
	lastSessionMu.RLock()
	id := globalLastProjectID
	lastSessionMu.RUnlock()

	if id != "" {
		return id
	}

	if redisClient != nil {
		id = redisClient.GetLastAntigravityProjectID()
		if id != "" {
			lastSessionMu.Lock()
			globalLastProjectID = id
			lastSessionMu.Unlock()
			return id
		}
	}
	return ""
}

func InitAntigravityBotStartup() {
	if redisClient == nil {
		return
	}
	lastSess := redisClient.GetLastAntigravitySessionID()
	lastProj := redisClient.GetLastAntigravityProjectID()

	lastSessionMu.Lock()
	if lastSess != "" {
		globalLastSessionID = lastSess
	}
	if lastProj != "" {
		globalLastProjectID = lastProj
	}
	lastSessionMu.Unlock()

	sessions := redisClient.LoadAllAntigravitySessionsFromRedis()
	antigravitySessionsMu.Lock()
	for _, s := range sessions {
		if s != nil && s.SessionID != "" {
			antigravitySessions[s.SessionID] = s
		}
	}
	antigravitySessionsMu.Unlock()

	slog.Info("Loaded Antigravity sessions on startup", "last_session", globalLastSessionID, "last_project", globalLastProjectID, "total_sessions", len(sessions))
}

func StartAntigravityRecoveryWorker(s *discordgo.Session) {
	antigravitySessionsMu.RLock()
	var runningSessions []*AntigravitySessionState
	for _, sess := range antigravitySessions {
		if sess != nil && sess.Status == "running" {
			runningSessions = append(runningSessions, sess)
		}
	}
	antigravitySessionsMu.RUnlock()

	if len(runningSessions) == 0 {
		slog.Info("No running Antigravity sessions to recover on startup")
		return
	}

	slog.Info("Starting Antigravity recovery worker for running sessions", "count", len(runningSessions))
	for _, sess := range runningSessions {
		go recoverRunningAntigravitySession(s, sess)
	}
}

func recoverRunningAntigravitySession(s *discordgo.Session, sessState *AntigravitySessionState) {
	sessID := sessState.SessionID
	slog.Info("Recovering session in background", "session_id", sessID, "turn_id", sessState.TurnID)

	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()

	timeout := time.After(15 * time.Minute)
	var lastFingerprint string

	for {
		select {
		case <-timeout:
			slog.Warn("Recovery worker timed out", "session_id", sessID)
			sessState.mu.Lock()
			if sessState.Status == "running" {
				sessState.Status = "error"
				sessState.ErrorMessage = "봇 재기동 후 복구 제한시간(15분)이 초과되었습니다."
			}
			sessState.mu.Unlock()
			updateDiscordRecoveredMessage(s, sessState)
			return

		case <-ticker.C:
			info, err := FetchProxySessionStatus(sessID)
			if err != nil {
				slog.Debug("Proxy session not found during recovery polling", "session_id", sessID, "error", err)
				continue
			}

			sessState.mu.Lock()
			if len(info.Tools) > 0 {
				sessState.RawToolNames = info.Tools
			}
			if len(info.Thinking) > 0 {
				sessState.ThinkingLogs = info.Thinking
			}
			if len(info.Chat) > 0 {
				sessState.ChatLogs = info.Chat
			}
			if info.FinalResponse != "" {
				sessState.FinalResponse = CleanResultOutput(info.FinalResponse)
			}
			if info.ErrorMessage != "" {
				sessState.ErrorMessage = info.ErrorMessage
			}
			if info.Usage.TotalTokens > 0 || info.Usage.InputTokens > 0 {
				sessState.Usage = info.Usage
			}
			sessState.Status = info.Status

			status := sessState.Status
			toolsLen := len(sessState.RawToolNames)
			thinkingLen := len(sessState.ThinkingLogs)
			chatLen := len(sessState.ChatLogs)
			finalLen := len(sessState.FinalResponse)
			errLen := len(sessState.ErrorMessage)
			usageTokens := sessState.Usage.TotalTokens + sessState.Usage.InputTokens
			sessState.mu.Unlock()

			fingerprint := fmt.Sprintf("%s:%d:%d:%d:%d:%d:%d", status, thinkingLen, chatLen, toolsLen, finalLen, errLen, usageTokens)
			if fingerprint != lastFingerprint || (!info.IsRunning && (status == "completed" || status == "error" || status == "cancelled")) {
				lastFingerprint = fingerprint
				updateDiscordRecoveredMessage(s, sessState)

				if !info.IsRunning && (status == "completed" || status == "error" || status == "cancelled") {
					slog.Info("Successfully recovered and completed session message", "session_id", sessID, "status", status)
					if redisClient != nil {
						_ = redisClient.SaveAntigravitySessionState(sessState)
					}
					return
				}
			}
		}
	}
}

func updateDiscordRecoveredMessage(s *discordgo.Session, sessState *AntigravitySessionState) {
	_, followupParts, components := buildAntigravityMessageContent(sessState)

	sessState.mu.Lock()
	appID := sessState.AppID
	if appID == "" && s.State != nil && s.State.User != nil {
		appID = s.State.User.ID
	}
	token := sessState.InteractionToken
	channelID := sessState.ChannelID
	msgID := sessState.MessageID
	status := sessState.Status
	userID := sessState.UserID
	sessState.mu.Unlock()

	var editErr error = fmt.Errorf("no edit method executed")

	// 1. PRIMARY METHOD: Webhook PATCH @original (Works for User-Installed Apps & Server Bots within 15min)
	if appID != "" && token != "" {
		uri := discordgo.EndpointWebhookMessage(appID, token, "@original")
		data := WebhookEditComponentsV2{
			Components: &components,
			Flags:      discordgo.MessageFlagsIsComponentsV2,
		}
		response, err := s.RequestWithBucketID("PATCH", uri, data, discordgo.EndpointWebhookToken("", ""))
		if err == nil {
			editErr = nil
			var msg *discordgo.Message
			if json.Unmarshal(response, &msg) == nil && msg != nil && msg.ID != "" {
				sessState.mu.Lock()
				sessState.MessageID = msg.ID
				sessState.mu.Unlock()
			}
		} else {
			editErr = err
		}
	}

	// 2. SECONDARY METHOD: ChannelMessageEditComplex
	if editErr != nil && channelID != "" && msgID != "" {
		_, err := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
			Channel:    channelID,
			ID:         msgID,
			Flags:      discordgo.MessageFlagsIsComponentsV2,
			Components: &components,
		})
		if err == nil {
			editErr = nil
		}
	}

	// 3. TERTIARY METHOD: FollowupMessage or ChannelMessageSend on completion
	if editErr != nil && (status == "completed" || status == "error") {
		if appID != "" && token != "" {
			_, err := s.FollowupMessageCreate(&discordgo.Interaction{AppID: appID, Token: token}, true, &discordgo.WebhookParams{
				Flags:      discordgo.MessageFlagsIsComponentsV2,
				Components: components,
			})
			if err == nil {
				editErr = nil
			}
		}
		if editErr != nil && channelID != "" {
			_, err := s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
				Flags:      discordgo.MessageFlagsIsComponentsV2,
				Components: components,
			})
			if err == nil {
				editErr = nil
			}
		}
	}

	// Send remaining long markdown parts as followups & completion ping & attachments!
	if status == "completed" || status == "error" {
		if status == "completed" {
			attachedFiles := extractReferencedFiles(sessState)
			if len(attachedFiles) > 0 {
				fileHeader := fmt.Sprintf("**생성된 결과물 / 첨부파일 (%d개)**", len(attachedFiles))
				if appID != "" && token != "" {
					_, _ = s.FollowupMessageCreate(&discordgo.Interaction{AppID: appID, Token: token}, true, &discordgo.WebhookParams{
						Content: fileHeader,
						Files:   attachedFiles,
					})
				} else if channelID != "" {
					_, _ = s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
						Content: fileHeader,
						Files:   attachedFiles,
					})
				}
			}

			if len(followupParts) > 0 {
				for _, fp := range followupParts {
					if strings.TrimSpace(fp) != "" {
						if appID != "" && token != "" {
							_, _ = s.FollowupMessageCreate(&discordgo.Interaction{AppID: appID, Token: token}, true, &discordgo.WebhookParams{
								Content: fp,
							})
						} else if channelID != "" {
							_, _ = s.ChannelMessageSend(channelID, fp)
						}
					}
				}
			}
		}

		if userID != "" {
			pingContent := fmt.Sprintf("<@%s> 작업이 완료되었습니다.", userID)
			if status == "error" {
				pingContent = fmt.Sprintf("<@%s> 작업 중 오류가 발생했습니다.", userID)
			}

			if appID != "" && token != "" {
				_, _ = s.FollowupMessageCreate(&discordgo.Interaction{AppID: appID, Token: token}, true, &discordgo.WebhookParams{
					Content: pingContent,
				})
			} else if channelID != "" {
				_, _ = s.ChannelMessageSend(channelID, pingContent)
			}
		}
	}
}

func downloadAttachmentToWorkspace(projectID string, att *discordgo.MessageAttachment) (string, error) {
	if att == nil || att.URL == "" {
		return "", nil
	}
	if projectID == "" {
		projectID = "default"
	}
	targetDir := fmt.Sprintf("/mnt/antigravity_workspaces/%s/uploads", projectID)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return "", err
	}
	cleanFilename := filepath.Base(att.Filename)
	if cleanFilename == "." || cleanFilename == "/" || cleanFilename == "" {
		cleanFilename = fmt.Sprintf("file_%d", time.Now().Unix())
	}
	targetPath := filepath.Join(targetDir, cleanFilename)

	resp, err := http.Get(att.URL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	out, err := os.Create(targetPath)
	if err != nil {
		return "", err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return "", err
	}
	return targetPath, nil
}

func buildAntigravityCommand() (BotCommand, error) {
	return NewBotCommandBuilder("antigravity").
		WithDescription(".").
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionSubCommand,
			Name:        "ask",
			Description: ".",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "prompt",
					Description: ".",
					Required:    true,
				},
				{
					Type:         discordgo.ApplicationCommandOptionString,
					Name:         "project",
					Description:  ".",
					Required:     false,
					Autocomplete: true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "model",
					Description: ".",
					Required:    false,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "Gemini 3.8 Flash (High) [기본값]", Value: "gemini-3.8-flash-high"},
						{Name: "Gemini 3.8 Flash (Medium)", Value: "gemini-3.8-flash-medium"},
						{Name: "Gemini 3.8 Flash (Low)", Value: "gemini-3.8-flash-low"},
						{Name: "Gemini 3.7 Flash (High)", Value: "gemini-3.7-flash-high"},
						{Name: "Gemini 3.7 Flash (Medium)", Value: "gemini-3.7-flash-medium"},
						{Name: "Gemini 3.7 Flash (Low)", Value: "gemini-3.7-flash-low"},
						{Name: "Gemini 3.1 Pro (High)", Value: "gemini-3.1-pro-high"},
						{Name: "Gemini 3.1 Pro (Low)", Value: "gemini-3.1-pro-low"},
						{Name: "Claude Sonnet 4.6 (Thinking)", Value: "claude-sonnet-4-6"},
						{Name: "Claude Opus 4.6 (Thinking)", Value: "claude-opus-4-6-thinking"},
						{Name: "GPT-OSS 120B (Medium)", Value: "gpt-oss-120b-medium"},
						{Name: "Gemini 3.6 Flash (High)", Value: "gemini-3.6-flash-high"},
						{Name: "Gemini 3.5 Flash (High)", Value: "gemini-3.5-flash-high"},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionAttachment,
					Name:        "file",
					Description: ".",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionAttachment,
					Name:        "file2",
					Description: ".",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionAttachment,
					Name:        "file3",
					Description: ".",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionAttachment,
					Name:        "file4",
					Description: ".",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionAttachment,
					Name:        "file5",
					Description: ".",
					Required:    false,
				},
			},
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionSubCommand,
			Name:        "clear",
			Description: ".",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:         discordgo.ApplicationCommandOptionString,
					Name:         "project",
					Description:  ".",
					Required:     false,
					Autocomplete: true,
				},
			},
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionSubCommand,
			Name:        "compact",
			Description: ".",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:         discordgo.ApplicationCommandOptionString,
					Name:         "project",
					Description:  ".",
					Required:     false,
					Autocomplete: true,
				},
			},
		}).
		WithFunction(handleAntigravityCommand).
		Build()
}

func handleAntigravityCommand(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	// 1. Immediately send Deferred response with MessageFlagsIsComponentsV2
	err := s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsIsComponentsV2,
		},
	})
	if err != nil {
		slog.Error("Failed to send deferred interaction response for /antigravity", "error", err)
		return
	}

	// 2. Offload proxy communications & update loop to background goroutine
	go func() {
		subcommand := ""
		prompt := ""
		projectID := ""
		model := "gemini-3.8-flash-high"
		var attachment *discordgo.MessageAttachment

		options := ic.ApplicationCommandData().Options
		if len(options) > 0 {
			firstOpt := options[0]
			if firstOpt.Type == discordgo.ApplicationCommandOptionSubCommand {
				subcommand = firstOpt.Name
				for _, subOpt := range firstOpt.Options {
					if subOpt.Name == "prompt" {
						prompt = strings.TrimSpace(subOpt.StringValue())
					} else if subOpt.Name == "project" || subOpt.Name == "project_id" {
						projectID = strings.TrimSpace(subOpt.StringValue())
					} else if subOpt.Name == "model" {
						model = strings.TrimSpace(subOpt.StringValue())
					} else if subOpt.Name == "file" || subOpt.Name == "attachment" {
						attID := subOpt.Value.(string)
						if ic.ApplicationCommandData().Resolved != nil && ic.ApplicationCommandData().Resolved.Attachments != nil {
							attachment = ic.ApplicationCommandData().Resolved.Attachments[attID]
						}
					}
				}
			} else {
				for _, opt := range options {
					if opt.Name == "prompt" {
						prompt = strings.TrimSpace(opt.StringValue())
					} else if opt.Name == "project" || opt.Name == "project_id" {
						projectID = strings.TrimSpace(opt.StringValue())
					} else if opt.Name == "model" {
						model = strings.TrimSpace(opt.StringValue())
					} else if opt.Name == "file" || opt.Name == "attachment" {
						attID := opt.Value.(string)
						if ic.ApplicationCommandData().Resolved != nil && ic.ApplicationCommandData().Resolved.Attachments != nil {
							attachment = ic.ApplicationCommandData().Resolved.Attachments[attID]
						}
					}
				}
			}
		}

		if model == "" {
			model = "gemini-3.8-flash-high"
		}

		slog.Info("handleAntigravityCommand started", "subcommand", subcommand, "prompt", prompt, "project_id", projectID, "model", model, "has_file", attachment != nil)

		// 3. Handle /antigravity clear
		if subcommand == "clear" {
			targetProject := projectID
			if targetProject == "" {
				targetProject = GetLastAntigravityProjectID()
			}
			if targetProject == "" {
				targetProject = "default"
			}

			sessID := fmt.Sprintf("ag_proj_%s", targetProject)
			antigravitySessionsMu.Lock()
			delete(antigravitySessions, sessID)
			antigravitySessionsMu.Unlock()

			if redisClient != nil {
				_ = redisClient.ClearAntigravityProjectSession(targetProject)
			}
			ClearAntigravityProxySession(sessID)

			clearComponents := buildSimpleComponentsV2(
				"프로젝트 세션 초기화",
				fmt.Sprintf("**`%s`** 프로젝트의 대화 세션 및 기억이 깔끔하게 초기화되었습니다.\n다음 질문부터 새로운 대화가 시작됩니다.", targetProject),
				fmt.Sprintf("프로젝트: %s", targetProject),
			)
			_, editErr := editInteractionComponentsV2(s, ic, clearComponents)
			if editErr != nil {
				slog.Error("Failed to send clear response", "error", editErr)
			}
			return
		}

		// 4. Handle /antigravity compact
		if subcommand == "compact" {
			targetProject := projectID
			if targetProject == "" {
				targetProject = GetLastAntigravityProjectID()
			}
			if targetProject == "" {
				targetProject = "default"
			}

			sessID := fmt.Sprintf("ag_proj_%s", targetProject)
			loadingComponents := buildSimpleComponentsV2(
				"세션 컨텍스트 압축 중...",
				fmt.Sprintf("**`%s`** 프로젝트의 과거 대화 및 파일 작업 기록을 요약 압축하고 있습니다.\n잠시만 기다려 주세요...", targetProject),
				fmt.Sprintf("프로젝트: %s", targetProject),
			)
			_, _ = editInteractionComponentsV2(s, ic, loadingComponents)

			summary, err := CompactAntigravityProxySession(sessID, targetProject)
			if err != nil {
				errComponents := buildSimpleComponentsV2(
					"컨텍스트 압축 실패",
					fmt.Sprintf("압축 중 오류가 발생했습니다: %v", err),
					fmt.Sprintf("프로젝트: %s", targetProject),
				)
				_, _ = editInteractionComponentsV2(s, ic, errComponents)
				return
			}

			compactMsg := fmt.Sprintf("**`%s`** 프로젝트 세션 압축(Compact)이 완료되었습니다!\n과거 누적된 토큰을 정리하고 핵심 맥락과 파일 상태를 새로운 세션에 성공적으로 이식했습니다.\n\n%s", targetProject, summary)
			compactComponents := buildSimpleComponentsV2(
				"세션 컨텍스트 압축 완료",
				compactMsg,
				fmt.Sprintf("프로젝트: %s", targetProject),
			)
			_, _ = editInteractionComponentsV2(s, ic, compactComponents)
			return
		}

		if prompt == "" {
			emptyComponents := buildSimpleComponentsV2(
				"입력 오류",
				"질문 또는 요청 내용(`prompt`)을 입력해 주세요.",
				"",
			)
			_, _ = editInteractionComponentsV2(s, ic, emptyComponents)
			return
		}

		// 4. Determine ProjectID
		if projectID == "" {
			projectID = GetLastAntigravityProjectID()
		}
		if projectID == "" {
			projectID = "default"
		}
		SetLastAntigravityProjectID(projectID)

		// Attachment payload for proxy (support multiple attachments)
		var filesPayload []ProxyFileAttachment
		if ic.ApplicationCommandData().Resolved != nil && ic.ApplicationCommandData().Resolved.Attachments != nil {
			for _, att := range ic.ApplicationCommandData().Resolved.Attachments {
				if att != nil && att.URL != "" {
					filesPayload = append(filesPayload, ProxyFileAttachment{
						Name: att.Filename,
						URL:  att.URL,
						Size: att.Size,
					})
				}
			}
		}

		// 5. Project-based Session ID & Turn ID
		sessionID := fmt.Sprintf("ag_proj_%s", projectID)
		turnID := fmt.Sprintf("turn_%d", time.Now().UnixNano())
		SetLastAntigravitySessionID(sessionID)

		userID := ""
		if ic.Member != nil && ic.Member.User != nil {
			userID = ic.Member.User.ID
		} else if ic.User != nil {
			userID = ic.User.ID
		}

		appID := ""
		token := ""
		if ic.Interaction != nil {
			appID = ic.Interaction.AppID
			token = ic.Interaction.Token
		}
		if appID == "" && s.State != nil && s.State.User != nil {
			appID = s.State.User.ID
		}

		title := extractShortTitle(prompt)
		sessState := &AntigravitySessionState{
			SessionID:        sessionID,
			ProjectID:        projectID,
			TurnID:           turnID,
			Model:            model,
			UserID:           userID,
			AppID:            appID,
			InteractionToken: token,
			Title:            title,
			Prompt:           prompt,
			ChannelID:        ic.ChannelID,
			Status:           "running",
		}
		RegisterAntigravitySession(sessState)

		_, _, initialComponents := buildAntigravityMessageContent(sessState)
		msg, editErr := editInteractionComponentsV2(s, ic, initialComponents)
		if editErr != nil {
			slog.Error("Failed initial interaction response edit for /antigravity", "error", editErr)
		} else if msg != nil {
			sessState.mu.Lock()
			sessState.MessageID = msg.ID
			sessState.mu.Unlock()
			if redisClient != nil {
				_ = redisClient.SaveAntigravitySessionState(sessState)
			}
		}

		streamCh, cancelFunc, err := SendAntigravityChatStream(sessionID, projectID, prompt, turnID, model, filesPayload)
		if err != nil {
			slog.Error("Failed to connect to antigravity HTTP proxy stream", "error", err)
			errComponents := buildSimpleComponentsV2(
				"프록시 연결 실패",
				fmt.Sprintf("프록시 서버 통신 오류: %v", err),
				"",
			)
			_, _ = editInteractionComponentsV2(s, ic, errComponents)
			return
		}
		sessState.CancelFunc = cancelFunc

		// Stream consumer goroutine
		go func() {
			for pResp := range streamCh {
				sessState.mu.Lock()
				switch pResp.Type {
				case "thinking":
					sessState.ThinkingLogs = append(sessState.ThinkingLogs, pResp.Output)
				case "tool":
					if pResp.Tool != "" {
						sessState.RawToolNames = append(sessState.RawToolNames, pResp.Tool)
					}
				case "chat":
					newText := pResp.Output
					if newText != "" {
						currentAll := strings.Join(sessState.ChatLogs, "")
						if currentAll == "" {
							sessState.ChatLogs = []string{newText}
						} else if currentAll == newText || strings.Contains(currentAll, newText) {
							// Skip duplicate
						} else if strings.Contains(newText, currentAll) {
							sessState.ChatLogs = []string{newText}
						} else {
							// Overlap check
							overlapLen := 0
							rCurrent := []rune(currentAll)
							rNew := []rune(newText)
							maxOverlap := 200
							if len(rCurrent) < maxOverlap {
								maxOverlap = len(rCurrent)
							}
							if len(rNew) < maxOverlap {
								maxOverlap = len(rNew)
							}
							for l := 1; l <= maxOverlap; l++ {
								if string(rCurrent[len(rCurrent)-l:]) == string(rNew[:l]) {
									overlapLen = l
								}
							}
							if overlapLen > 0 {
								sessState.ChatLogs = append(sessState.ChatLogs, string(rNew[overlapLen:]))
							} else {
								sessState.ChatLogs = append(sessState.ChatLogs, newText)
							}
						}
					}
				case "result":
					newText := CleanResultOutput(pResp.Output)
					if newText != "" {
						sessState.FinalResponse = newText
						sessState.ChatLogs = []string{newText}
						sessState.ThinkingLogs = nil
						sessState.ErrorMessage = ""
					}
					sessState.Status = "completed"
				case "usage":
					var u TokenUsage
					if err := json.Unmarshal([]byte(pResp.Output), &u); err == nil {
						sessState.Usage = u
					}
				case "completed":
					sessState.Status = "completed"
					if sessState.FinalResponse == "" && len(sessState.ChatLogs) > 0 {
						sessState.FinalResponse = CleanResultOutput(strings.Join(sessState.ChatLogs, ""))
						sessState.ThinkingLogs = nil
					}
					if sessState.FinalResponse != "" {
						sessState.ErrorMessage = ""
					}
				case "cancelled":
					sessState.Status = "cancelled"
				case "fatal_error":
					sessState.Status = "error"
					sessState.ErrorMessage = pResp.Output
					if sessState.FinalResponse == "" && len(sessState.ChatLogs) > 0 {
						sessState.FinalResponse = CleanResultOutput(strings.Join(sessState.ChatLogs, ""))
						sessState.ThinkingLogs = nil
					}
				case "error":
					errOut := pResp.Output
					if strings.Contains(errOut, "context canceled") ||
						strings.Contains(errOut, "manage_task") ||
						strings.Contains(errOut, "Step was canceled") ||
						strings.Contains(errOut, "hardcoded system protection boundary rule") {
						// Ignore internal cancellation
					} else {
						slog.Warn("Intermediate tool warning received", "session_id", sessState.SessionID, "error", errOut)
					}
				}
				sessState.mu.Unlock()
			}

			sessState.mu.Lock()
			if sessState.Status == "running" {
				if sessState.FinalResponse != "" || len(sessState.ChatLogs) > 0 {
					sessState.Status = "completed"
					if sessState.FinalResponse == "" {
						sessState.FinalResponse = CleanResultOutput(strings.Join(sessState.ChatLogs, ""))
					}
					sessState.ThinkingLogs = nil
					sessState.ErrorMessage = ""
				} else if sessState.ErrorMessage != "" {
					sessState.Status = "error"
				} else {
					sessState.Status = "completed"
				}
			} else if sessState.FinalResponse != "" {
				sessState.ErrorMessage = ""
			}
			sessState.mu.Unlock()

			if redisClient != nil {
				_ = redisClient.SaveAntigravitySessionState(sessState)
			}
		}()

		// Start background update loop
		runAntigravityUpdateLoop(s, ic, sessState)
	}()
}

func splitMarkdownContent(text string, maxLen int) []string {
	runes := []rune(text)
	if len(runes) <= maxLen {
		return []string{text}
	}

	var parts []string
	remaining := text

	for len([]rune(remaining)) > 0 {
		r := []rune(remaining)
		if len(r) <= maxLen {
			parts = append(parts, remaining)
			break
		}

		cutIndex := maxLen
		subStr := string(r[:maxLen])

		if lastPara := strings.LastIndex(subStr, "\n\n"); lastPara > maxLen/2 {
			cutIndex = len([]rune(subStr[:lastPara]))
		} else if lastLine := strings.LastIndex(subStr, "\n"); lastLine > maxLen/2 {
			cutIndex = len([]rune(subStr[:lastLine]))
		} else if lastSpace := strings.LastIndex(subStr, " "); lastSpace > maxLen/2 {
			cutIndex = len([]rune(subStr[:lastSpace]))
		}

		part := string(r[:cutIndex])
		backtickCount := strings.Count(part, "```")
		if backtickCount%2 != 0 {
			part += "\n```"
		}
		parts = append(parts, strings.TrimSpace(part))

		remStr := strings.TrimSpace(string(r[cutIndex:]))
		if backtickCount%2 != 0 {
			remStr = "```\n" + remStr
		}
		remaining = remStr
	}

	return parts
}

func runAntigravityUpdateLoop(s *discordgo.Session, ic *discordgo.InteractionCreate, sessState *AntigravitySessionState) {
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()

	var lastFingerprint string
	hasSentFollowups := false

	for {
		select {
		case <-ticker.C:
			sessState.mu.Lock()
			status := sessState.Status
			msgID := sessState.MessageID
			channelID := sessState.ChannelID
			thinkingLen := len(sessState.ThinkingLogs)
			chatLen := len(sessState.ChatLogs)
			toolsLen := len(sessState.RawToolNames)
			finalLen := len(sessState.FinalResponse)
			errLen := len(sessState.ErrorMessage)
			usageTokens := sessState.Usage.TotalTokens + sessState.Usage.InputTokens
			sessState.mu.Unlock()

			fingerprint := fmt.Sprintf("%s:%d:%d:%d:%d:%d:%d", status, thinkingLen, chatLen, toolsLen, finalLen, errLen, usageTokens)

			if fingerprint != lastFingerprint || status == "completed" || status == "cancelled" || status == "error" {
				_, followupParts, components := buildAntigravityMessageContent(sessState)

				var err error
				if ic != nil && ic.Interaction != nil {
					var msg *discordgo.Message
					msg, err = editInteractionComponentsV2(s, ic, components)
					if err == nil && msg != nil && msg.ID != "" {
						sessState.mu.Lock()
						needSave := (sessState.MessageID != msg.ID)
						sessState.MessageID = msg.ID
						sessState.mu.Unlock()
						if needSave && redisClient != nil {
							_ = redisClient.SaveAntigravitySessionState(sessState)
						}
					}
				}

				if err != nil {
					slog.Warn("editInteractionComponentsV2 failed, trying Followup or ChannelMessageEdit", "session_id", sessState.SessionID, "error", err)

					// 1. ChannelMessageEditComplex fallback if we have channelID and messageID
					if channelID != "" && msgID != "" {
						_, err = s.ChannelMessageEditComplex(&discordgo.MessageEdit{
							Channel:    channelID,
							ID:         msgID,
							Flags:      discordgo.MessageFlagsIsComponentsV2,
							Components: &components,
						})
					}

					// 2. If message was deleted by another admin, recreate message via FollowupMessageCreate
					if err != nil && ic.Interaction != nil {
						var followupMsg *discordgo.Message
						followupMsg, err = s.FollowupMessageCreate(ic.Interaction, true, &discordgo.WebhookParams{
							Flags:      discordgo.MessageFlagsIsComponentsV2,
							Components: components,
						})
						if err == nil && followupMsg != nil {
							sessState.mu.Lock()
							sessState.MessageID = followupMsg.ID
							sessState.mu.Unlock()
							if redisClient != nil {
								_ = redisClient.SaveAntigravitySessionState(sessState)
							}
							slog.Info("Successfully recreated antigravity response as FollowupMessage", "session_id", sessState.SessionID, "message_id", followupMsg.ID)
						}
					}

					// 3. Recreate via ChannelMessageSendComplex
					if err != nil && channelID != "" {
						var sendMsg *discordgo.Message
						sendMsg, err = s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
							Flags:      discordgo.MessageFlagsIsComponentsV2,
							Components: components,
						})
						if err == nil && sendMsg != nil {
							sessState.mu.Lock()
							sessState.MessageID = sendMsg.ID
							sessState.mu.Unlock()
							if redisClient != nil {
								_ = redisClient.SaveAntigravitySessionState(sessState)
							}
							slog.Info("Successfully recreated antigravity response as new channel message", "session_id", sessState.SessionID, "message_id", sendMsg.ID)
						}
					}

					// 4. Fallback: if server channel permissions blocked, fallback to user's private DM
					if err != nil && sessState.UserID != "" {
						dmChan, dmErr := s.UserChannelCreate(sessState.UserID)
						if dmErr == nil && dmChan != nil {
							var dmMsg *discordgo.Message
							dmMsg, err = s.ChannelMessageSendComplex(dmChan.ID, &discordgo.MessageSend{
								Flags:      discordgo.MessageFlagsIsComponentsV2,
								Components: components,
							})
							if err == nil && dmMsg != nil {
								sessState.mu.Lock()
								sessState.ChannelID = dmChan.ID
								sessState.MessageID = dmMsg.ID
								sessState.mu.Unlock()
								if redisClient != nil {
									_ = redisClient.SaveAntigravitySessionState(sessState)
								}
								slog.Info("Successfully redirected antigravity response to user DM due to channel permission error", "session_id", sessState.SessionID)
							}
						}
					}
				}

				if err != nil {
					slog.Error("Failed all methods to update antigravity message", "session_id", sessState.SessionID, "error", err)
				} else {
					lastFingerprint = fingerprint
					slog.Info("Successfully updated antigravity message", "session_id", sessState.SessionID, "status", status)
				}

				if status == "completed" || status == "cancelled" || status == "error" {
					// 1. Send generated files and artifacts as attachments on completion
					if status == "completed" && ic.Interaction != nil {
						attachedFiles := extractReferencedFiles(sessState)
						if len(attachedFiles) > 0 {
							fileHeader := fmt.Sprintf("**생성된 결과물 / 첨부파일 (%d개)**", len(attachedFiles))
							_, fileErr := s.FollowupMessageCreate(ic.Interaction, true, &discordgo.WebhookParams{
								Content: fileHeader,
								Files:   attachedFiles,
							})
							if fileErr != nil {
								slog.Warn("Failed to send attached files as followup", "error", fileErr)
							} else {
								slog.Info("Successfully attached files to Discord response", "count", len(attachedFiles))
							}
						}
					}

					// 2. Send long markdown chunks as followups
					if status == "completed" && len(followupParts) > 0 && !hasSentFollowups && ic.Interaction != nil {
						hasSentFollowups = true
						for _, fp := range followupParts {
							if strings.TrimSpace(fp) != "" {
								_, _ = s.FollowupMessageCreate(ic.Interaction, true, &discordgo.WebhookParams{
									Content: fp,
								})
							}
						}
					}

					// 3. Send completion ping notification to the user
					if (status == "completed" || status == "error") && sessState.UserID != "" && ic.Interaction != nil {
						pingContent := fmt.Sprintf("<@%s> 작업이 완료되었습니다.", sessState.UserID)
						if status == "error" {
							pingContent = fmt.Sprintf("<@%s> 작업 중 오류가 발생했습니다.", sessState.UserID)
						}
						_, _ = s.FollowupMessageCreate(ic.Interaction, true, &discordgo.WebhookParams{
							Content: pingContent,
						})
					}

					if redisClient != nil {
						_ = redisClient.SaveAntigravitySessionState(sessState)
					}
					return
				}
			}
		}
	}
}

func buildAntigravityMessageContent(sessState *AntigravitySessionState) (string, []string, []discordgo.MessageComponent) {
	sessState.mu.Lock()
	defer sessState.mu.Unlock()

	var followupParts []string

	// 1. Status Title & SubTitle (Prompt)
	title := "Antigravity Agent"
	if sessState.Status == "running" {
		title = "Antigravity Agent (작업 중...)"
	} else if sessState.Status == "error" {
		title = "Antigravity Agent (오류 발생)"
	} else if sessState.Status == "cancelled" {
		title = "Antigravity Agent (작업 중단됨)"
	}

	subTitle := sessState.Prompt

	// 2. Body Content (Thinking, Streaming, Final Response, Errors)
	var bodyText string
	hasMainBody := false

	if sessState.FinalResponse != "" {
		finalText := FormatDiscordMarkdown(sessState.FinalResponse)
		parts := splitMarkdownContent(finalText, 1800)
		if len(parts) > 0 {
			bodyText = parts[0]
			hasMainBody = true
		}
		if len(parts) > 1 {
			for _, p := range parts[1:] {
				moreParts := splitMarkdownContent(p, 1900)
				followupParts = append(followupParts, moreParts...)
			}
		}
	} else {
		// In-progress / Streaming state
		// 1. Priority to streaming chat response
		if len(sessState.ChatLogs) > 0 {
			chatCombined := strings.Join(sessState.ChatLogs, "")
			formattedChat := FormatDiscordMarkdown(chatCombined)
			parts := splitMarkdownContent(formattedChat, 1800)
			if len(parts) > 0 {
				bodyText = parts[0]
				hasMainBody = true
			}
		}

		// 2. If no chat text yet, display the latest thinking log
		if !hasMainBody && len(sessState.ThinkingLogs) > 0 {
			thinkingCombined := strings.Join(sessState.ThinkingLogs, "\n")
			formattedThinking := FormatThinkingMarkdown(thinkingCombined, 500)
			if formattedThinking != "" {
				bodyText = formattedThinking
				hasMainBody = true
			}
		}
	}

	// 3. Fatal Error message (Only display when the task actually failed/aborted)
	if sessState.Status == "error" && sessState.ErrorMessage != "" {
		if !hasMainBody {
			bodyText = fmt.Sprintf("**오류 발생**:\n```\n%s\n```", sessState.ErrorMessage)
			hasMainBody = true
		} else {
			bodyText += fmt.Sprintf("\n\n**작업 중단 오류**: `%s`", sessState.ErrorMessage)
		}
	}

	// 4. Cancelled or In-progress fallback
	if sessState.Status == "cancelled" {
		cancelNotice := "**사용자 요청에 의해 작업이 중단되었습니다.**"
		if hasMainBody {
			bodyText += "\n\n" + cancelNotice
		} else {
			bodyText = cancelNotice
			hasMainBody = true
		}
	} else if !hasMainBody {
		if sessState.Status == "running" {
			if len(sessState.RawToolNames) > 0 {
				bodyText = fmt.Sprintf("**에이전트 작업 수행 중...** (도구 %d회 실행 완료)", len(sessState.RawToolNames))
			} else if len(sessState.ThinkingLogs) > 0 {
				bodyText = "**에이전트 생각 중...**"
			} else {
				bodyText = "**에이전트 답변 생성 중...**"
			}
		} else {
			bodyText = "**출력 없음** (에이전트가 결과 텍스트를 남기지 않았거나 프로세스가 중간에 종료되었습니다)"
		}
	}

	// 5. Footer Text
	footerTools := formatCompressedTools(sessState.RawToolNames)
	projName := sessState.ProjectID
	if projName == "" {
		projName = "default"
	}
	modelName := sessState.Model
	if modelName == "" {
		modelName = "gemini-3.8-flash-high"
	}
	footerText := ""
	if footerTools != "" {
		footerText = fmt.Sprintf("%s | %s • %s", footerTools, projName, modelName)
	} else {
		footerText = fmt.Sprintf("사용된 도구 없음 | %s • %s", projName, modelName)
	}

	if sessState.Usage.TotalTokens > 0 || sessState.Usage.InputTokens > 0 {
		u := sessState.Usage
		total := u.TotalTokens
		if total == 0 {
			total = u.InputTokens + u.OutputTokens + u.ThinkingTokens
		}
		usageLine := fmt.Sprintf("사용 토큰: Total %s (Input: %s | Output: %s | Thinking: %s | Cache Read: %s)",
			formatNumberWithCommas(total),
			formatNumberWithCommas(u.InputTokens),
			formatNumberWithCommas(u.OutputTokens),
			formatNumberWithCommas(u.ThinkingTokens),
			formatNumberWithCommas(u.CacheReadTokens),
		)
		footerText = footerText + "\n" + usageLine
	}

	// 6. Action Button (Cancel)
	cancelBtn := discordgo.Button{
		Label:    "중단 (Cancel)",
		Style:    discordgo.DangerButton,
		CustomID: fmt.Sprintf("ag_cancel:%s:%s", sessState.SessionID, sessState.TurnID),
		Disabled: sessState.Status != "running",
	}

	builder := NewComponentsBuilder().
		WithTitle(title).
		WithSubTitle(subTitle).
		WithBody(bodyText).
		WithFooter(footerText).
		WithButtons(cancelBtn)

	components := builder.Build()
	return "", followupParts, components
}

func formatNumberWithCommas(n int) string {
	in := fmt.Sprintf("%d", n)
	var out []rune
	l := len(in)
	for i, r := range in {
		out = append(out, r)
		if (l-1-i)%3 == 0 && i != l-1 {
			out = append(out, ',')
		}
	}
	return string(out)
}

func formatCompressedTools(tools []string) string {
	if len(tools) == 0 {
		return ""
	}

	type toolCount struct {
		name  string
		count int
	}

	var counts []toolCount
	for _, t := range tools {
		if len(counts) > 0 && counts[len(counts)-1].name == t {
			counts[len(counts)-1].count++
		} else {
			counts = append(counts, toolCount{name: t, count: 1})
		}
	}

	var parts []string
	for _, tc := range counts {
		parts = append(parts, fmt.Sprintf("%s(x%d)", tc.name, tc.count))
	}

	return fmt.Sprintf("**사용된 도구**: %s", strings.Join(parts, " -> "))
}

func sendAntigravityProjectAutocomplete(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	focusInput := ""
	for _, opt := range ic.ApplicationCommandData().Options {
		if opt.Type == discordgo.ApplicationCommandOptionSubCommand {
			for _, subOpt := range opt.Options {
				if (subOpt.Name == "project" || subOpt.Name == "project_id") && subOpt.Focused {
					focusInput = strings.TrimSpace(subOpt.StringValue())
					break
				}
			}
		} else if (opt.Name == "project" || opt.Name == "project_id") && opt.Focused {
			focusInput = strings.TrimSpace(opt.StringValue())
			break
		}
	}

	choices := []*discordgo.ApplicationCommandOptionChoice{}
	seen := make(map[string]bool)
	lowFocus := strings.ToLower(focusInput)

	if focusInput == "" {
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
			Name:  "default (기본 프로젝트)",
			Value: "default",
		})
		seen["default"] = true
	} else if strings.Contains("default", lowFocus) {
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
			Name:  "default (기본 프로젝트)",
			Value: "default",
		})
		seen["default"] = true
	}

	// 1. Try reading directly from mounted directory
	entries, err := os.ReadDir("/mnt/antigravity_workspaces")
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				name := entry.Name()
				if !seen[name] {
					seen[name] = true
					if focusInput == "" || strings.Contains(strings.ToLower(name), lowFocus) {
						choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
							Name:  name,
							Value: name,
						})
					}
				}
			}
		}
	}

	// 2. Also fetch from HTTP proxy if any missing
	proxyProjects := FetchProxyProjects()
	for _, name := range proxyProjects {
		if !seen[name] {
			seen[name] = true
			if focusInput == "" || strings.Contains(strings.ToLower(name), lowFocus) {
				choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
					Name:  name,
					Value: name,
				})
			}
		}
	}

	// 3. If focusInput is not empty and not an existing project, offer to create it
	if focusInput != "" && !seen[focusInput] {
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
			Name:  fmt.Sprintf("새 프로젝트 생성: %s", focusInput),
			Value: focusInput,
		})
	}

	if len(choices) > 25 {
		choices = choices[:25]
	}

	_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionApplicationCommandAutocompleteResult,
		Data: &discordgo.InteractionResponseData{
			Choices: choices,
		},
	})
}

func handleAntigravityButton(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	customID := ic.MessageComponentData().CustomID
	parts := strings.Split(customID, ":")
	if len(parts) < 2 {
		return
	}

	action := parts[0]
	sessionID := parts[1]
	turnID := ""
	if len(parts) >= 3 {
		turnID = parts[2]
	}

	sessState := GetAntigravitySession(sessionID)
	if sessState == nil {
		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "활성화된 세션을 찾을 수 없습니다.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	if action == "ag_cancel" {
		if sessState.UserID != "" && !isInteractionOwnerOrAdmin(ic, sessState.UserID) {
			_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "명령을 실행한 사용자 또는 관리자만 작업을 중단할 수 있습니다.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		sessState.mu.Lock()
		activeTurnID := sessState.TurnID
		activeStatus := sessState.Status
		sessState.mu.Unlock()

		// 1. If clicked on an older prompt's cancel button, ignore and do not kill active turn
		if turnID != "" && activeTurnID != "" && turnID != activeTurnID {
			_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "ℹ️ 이미 종료되었거나 이전 질문의 중단 버튼입니다. (현재 진행 중인 작업에 영향 없음)",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		// 2. If active session is already finished
		if activeStatus != "running" {
			_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "ℹ️ 현재 진행 중인 작업이 없습니다.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		// 3. Cancel the current active turn
		if sessState.CancelFunc != nil {
			sessState.CancelFunc()
		}
		sessState.mu.Lock()
		sessState.Status = "cancelled"
		sessState.mu.Unlock()

		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredMessageUpdate,
		})

		if redisClient != nil {
			_ = redisClient.SaveAntigravitySessionState(sessState)
		}
	}
}

func buildSimpleComponentsV2(title string, text string, footer string) []discordgo.MessageComponent {
	b := NewComponentsBuilder()
	if title != "" {
		b.WithTitle(title)
	}
	if text != "" {
		b.WithBody(FormatDiscordMarkdown(text))
	}
	if footer != "" {
		b.WithFooter(footer)
	}
	return b.Build()
}

func HandleAntigravityComponent(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	handleAntigravityButton(s, ic)
}

func HandleAntigravityModalSubmit(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	// Stub for modal submit if any
}

func extractReferencedFiles(sessState *AntigravitySessionState) []*discordgo.File {
	sessState.mu.Lock()
	text := sessState.FinalResponse
	if text == "" {
		text = strings.Join(sessState.ChatLogs, "")
	}
	sessID := sessState.SessionID
	sessState.mu.Unlock()

	if text == "" {
		return nil
	}

	foundPaths := make(map[string]bool)
	var fileList []string

	// 1. Regex for Markdown Images: ![caption](path)
	reImg := regexp.MustCompile(`!\[.*?\]\((?:file:\/\/)?([^\)\s]+)\)`)
	for _, m := range reImg.FindAllStringSubmatch(text, -1) {
		if len(m) >= 2 {
			p := strings.TrimSpace(m[1])
			if p != "" && !foundPaths[p] {
				foundPaths[p] = true
				fileList = append(fileList, p)
			}
		}
	}

	// 2. Regex for Markdown Links: [label](path) - ONLY for shared / uploads / brain artifacts or images
	reLink := regexp.MustCompile(`\[.*?\]\((?:file:\/\/)?(\/(?:home\/fedora|\.gemini|mnt\/antigravity_workspaces|tmp)[^\)\s]+)\)`)
	for _, m := range reLink.FindAllStringSubmatch(text, -1) {
		if len(m) >= 2 {
			p := strings.TrimSpace(m[1])
			isShared := strings.Contains(p, "/shared/") || strings.Contains(p, "/uploads/") || strings.Contains(p, "/brain/")
			ext := strings.ToLower(filepath.Ext(p))
			isImg := ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp" || ext == ".gif" || ext == ".svg"
			if (isShared || isImg) && !foundPaths[p] {
				foundPaths[p] = true
				fileList = append(fileList, p)
			}
		}
	}

	// 3. Scan conversation artifacts directory for files created in this turn
	convMapPath := "/tmp/antigravity_conv_map.json"
	if data, err := os.ReadFile(convMapPath); err == nil {
		var cMap map[string]string
		if json.Unmarshal(data, &cMap) == nil {
			if convUUID, ok := cMap[sessID]; ok && convUUID != "" {
				brainDir := filepath.Join("/home/fedora/.gemini/antigravity-cli/brain", convUUID)
				if entries, err := os.ReadDir(brainDir); err == nil {
					for _, e := range entries {
						if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
							fp := filepath.Join(brainDir, e.Name())
							ext := strings.ToLower(filepath.Ext(e.Name()))
							if (ext == ".jpg" || ext == ".png" || ext == ".webp" || ext == ".gif" || ext == ".svg" || ext == ".md" || ext == ".json") && !foundPaths[fp] {
								if strings.Contains(text, e.Name()) {
									foundPaths[fp] = true
									fileList = append(fileList, fp)
								}
							}
						}
					}
				}
				mediaDir := filepath.Join(brainDir, ".tempmediaStorage")
				if entries, err := os.ReadDir(mediaDir); err == nil {
					for _, e := range entries {
						if !e.IsDir() {
							fp := filepath.Join(mediaDir, e.Name())
							if strings.Contains(text, e.Name()) && !foundPaths[fp] {
								foundPaths[fp] = true
								fileList = append(fileList, fp)
							}
						}
					}
				}
			}
		}
	}

	var discordFiles []*discordgo.File
	for _, fp := range fileList {
		if len(discordFiles) >= 8 { // Discord max limit per message is 10
			break
		}
		cleanPath := strings.TrimPrefix(fp, "file://")
		stat, err := os.Stat(cleanPath)
		if err != nil || stat.IsDir() || stat.Size() > 24*1024*1024 || stat.Size() == 0 {
			continue
		}

		f, err := os.Open(cleanPath)
		if err != nil {
			continue
		}

		filename := filepath.Base(cleanPath)
		ext := strings.ToLower(filepath.Ext(filename))
		contentType := "application/octet-stream"
		switch ext {
		case ".jpg", ".jpeg":
			contentType = "image/jpeg"
		case ".png":
			contentType = "image/png"
		case ".webp":
			contentType = "image/webp"
		case ".gif":
			contentType = "image/gif"
		case ".svg":
			contentType = "image/svg+xml"
		case ".json":
			contentType = "application/json"
		case ".md", ".txt", ".go", ".py", ".sh", ".log", ".csv":
			contentType = "text/plain; charset=utf-8"
		case ".pdf":
			contentType = "application/pdf"
		}

		discordFiles = append(discordFiles, &discordgo.File{
			Name:        filename,
			ContentType: contentType,
			Reader:      f,
		})
	}

	return discordFiles
}
