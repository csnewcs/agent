package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type AIWebhookPayload struct {
	ChannelID     string `json:"channel_id"`
	SessionID     string `json:"session_id"`
	Query         string `json:"query"`
	MessageID     string `json:"message_id,omitempty"`
	UserID        string `json:"user_id,omitempty"`
	Username      string `json:"username,omitempty"`
	SentAt        string `json:"sent_at,omitempty"`
	Token         string `json:"token,omitempty"`
	ApplicationID string `json:"application_id,omitempty"`
	NeedTitle     bool   `json:"needTitle,omitempty"`
}

func handleAIInteraction(config *Config, session *discordgo.Session, ic *discordgo.InteractionCreate, query string, userEphemeral bool, hasEphemeralOpt bool) error {
	// 사용자가 명시적으로 ephemeral: true로 지정한 경우를 제외하고는
	// "생각 중..." 대기 상태를 채널에 전체 공개(Public)로 생성
	deferEphemeral := false
	if hasEphemeralOpt && userEphemeral {
		deferEphemeral = true
	}

	var responseData *discordgo.InteractionResponseData
	if deferEphemeral {
		responseData = &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
		}
	}

	// Defer response immediately to avoid 3-second timeout
	err := session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: responseData,
	})
	if err != nil {
		return err
	}

	var userID string
	if ic.Member != nil && ic.Member.User != nil {
		userID = ic.Member.User.ID
	} else if ic.User != nil {
		userID = ic.User.ID
	}

	formattedQuery := fmt.Sprintf("u: %s a: false l: undefined c: %s", userID, query)

	needTitle, _ := IsSessionTitleEmpty(db, getSessionID())

	payload := AIWebhookPayload{
		ChannelID:     ic.ChannelID,
		SessionID:     getSessionID(),
		Query:         formattedQuery,
		Token:         ic.Token,
		ApplicationID: session.State.User.ID,
		NeedTitle:     needTitle,
	}

	// attach interaction metadata: message id (interaction id), user and timestamp
	if ic.Interaction != nil {
		payload.MessageID = ic.Interaction.ID
	}
	if ic.Member != nil && ic.Member.User != nil {
		payload.UserID = ic.Member.User.ID
		payload.Username = ic.Member.User.Username
	} else if ic.User != nil {
		payload.UserID = ic.User.ID
		payload.Username = ic.User.Username
	}
	payload.SentAt = time.Now().UTC().Format(time.RFC3339)

	// try to locate the user's most recent message in the channel to react to
	var userMsgID string
	if userID != "" {
		msgs, err := session.ChannelMessages(payload.ChannelID, 50, "", "", "")
		if err == nil {
			for _, m := range msgs {
				if m.Author != nil && m.Author.ID == userID && !m.Author.Bot {
					userMsgID = m.ID
					break
				}
			}
		}
	}

	aiResponse, err := sendAIWebhook(config, payload)
	if err != nil {
		if userMsgID != "" {
			if addErr := session.MessageReactionAdd(payload.ChannelID, userMsgID, "❌"); addErr != nil {
				slog.Error("Failed to add failure reaction to interaction message", "error", addErr)
			}
		}
		errMsg := "AI 요청 처리에 실패했습니다."
		_, _ = session.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{
			Content: &errMsg,
		})
		return err
	}

	if userMsgID != "" {
		if addErr := session.MessageReactionAdd(payload.ChannelID, userMsgID, "✅"); addErr != nil {
			slog.Error("Failed to add success reaction to interaction message", "error", addErr)
		}
	}

	slog.Info("n8n interaction response", "raw", aiResponse)
	responseText, title := parseN8NResponse(aiResponse)
	if responseText == "" {
		responseText = "AI가 빈 답변을 반환했습니다. n8n 워크플로우를 확인해주세요."
	}
	if title != "" {
		_ = UpdateSessionTitle(db, getSessionID(), title)
		slog.Info("Updated session title", "session_id", getSessionID(), "title", title)
	}

	isFinalEphemeral := false
	if hasEphemeralOpt {
		isFinalEphemeral = userEphemeral
	} else {
		outputLen := len([]rune(responseText))
		if outputLen > 100 {
			isFinalEphemeral = true
		} else {
			isFinalEphemeral = false
		}
	}

	if deferEphemeral && !isFinalEphemeral {
		_ = session.InteractionResponseDelete(ic.Interaction)
		return sendSplitFollowupMessages(session, ic, responseText)
	}

	if !deferEphemeral && isFinalEphemeral {
		// 초기 대기가 공개(Public)인 상태에서는 디스코드 API 특성상 에러 없이 가장 안전하게 편집하여 응답
		return sendSplitInteractionMessages(session, ic, responseText, false)
	}

	return sendSplitInteractionMessages(session, ic, responseText, isFinalEphemeral)
}

func handleAIRequest(config *Config, session *discordgo.Session, message *discordgo.MessageCreate, query string) error {
	hasAttachment := len(message.Attachments) > 0
	attachmentURL := "undefined"
	if hasAttachment {
		attachmentURL = message.Attachments[0].URL
	}
	formattedQuery := fmt.Sprintf("u: %s a: %t l: %s c: %s", message.Author.ID, hasAttachment, attachmentURL, query)

	needTitle, _ := IsSessionTitleEmpty(db, getSessionID())

	payload := AIWebhookPayload{
		ChannelID: message.ChannelID,
		SessionID: getSessionID(),
		Query:     formattedQuery,
		NeedTitle: needTitle,
	}

	// attach message metadata
	if message != nil && message.Message != nil {
		payload.MessageID = message.ID
		if message.Author != nil {
			payload.UserID = message.Author.ID
			payload.Username = message.Author.Username
		}
		// message.Timestamp is a discordgo.Timestamp string — use it if available
		payload.SentAt = message.Timestamp.String()
	} else if message != nil {
		payload.MessageID = message.ID
		if message.Author != nil {
			payload.UserID = message.Author.ID
			payload.Username = message.Author.Username
		}
		payload.SentAt = time.Now().UTC().Format(time.RFC3339)
	}

	aiResponse, err := sendAIWebhook(config, payload)
	if err != nil {
		// try to add a failure reaction to the original message
		if message != nil {
			if addErr := session.MessageReactionAdd(message.ChannelID, message.ID, "❌"); addErr != nil {
				slog.Error("Failed to add failure reaction", "error", addErr)
			}
		}
		return err
	}

	// on success, add a check reaction
	if message != nil {
		if addErr := session.MessageReactionAdd(message.ChannelID, message.ID, "✅"); addErr != nil {
			slog.Error("Failed to add success reaction", "error", addErr)
		}
	}
	slog.Info("n8n message response", "raw", aiResponse)
	responseText, title := parseN8NResponse(aiResponse)
	if responseText == "" {
		responseText = "AI가 빈 답변을 반환했습니다. n8n 워크플로우를 확인해주세요."
	}
	if title != "" {
		_ = UpdateSessionTitle(db, getSessionID(), title)
		slog.Info("Updated session title", "session_id", getSessionID(), "title", title)
	}
	err = sendSplitChannelMessages(session, message.ChannelID, responseText)
	return err
}

func sendAIWebhook(config *Config, payload AIWebhookPayload) (string, error) {
	// sanitize payload strings to avoid invalid UTF-8 or control characters
	payload.Query = sanitizeString(payload.Query)
	payload.MessageID = sanitizeString(payload.MessageID)
	payload.UserID = sanitizeString(payload.UserID)
	payload.Username = sanitizeString(payload.Username)

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	// choose target webhook URL: in development prefer the test webhook if provided
	targetURL := config.N8NWebhookURL
	if config.Mode == "development" && config.TestWebhookURL != "" {
		targetURL = config.TestWebhookURL
	}

	req, err := http.NewRequest(http.MethodPost, targetURL, bytes.NewBuffer(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	// Increase timeout to 120s for AI processing
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("n8n webhook returned status %d", resp.StatusCode)
	}

	buf := new(bytes.Buffer)
	_, err = buf.ReadFrom(resp.Body)
	if err != nil {
		return "", err
	}

	return buf.String(), nil
}

func parseN8NResponse(body string) (string, string) {
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(body), &result); err == nil {
		var output string
		if out, ok := result["output"].(string); ok && out != "" {
			output = out
		} else if txt, ok := result["text"].(string); ok && txt != "" {
			output = txt
		} else if resp, ok := result["response"].(string); ok && resp != "" {
			output = resp
		} else {
			output = body
		}

		var title string
		if t, ok := result["title"].(string); ok {
			title = t
		}
		return output, title
	}
	return body, ""
}

// sanitizeString ensures the string is valid UTF-8 and strips control
// characters that can break JSON parsers (except tab, newline, carriage return).
func sanitizeString(s string) string {
	if s == "" {
		return s
	}
	// replace invalid UTF-8 sequences
	s = strings.ToValidUTF8(s, "")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\n' {
			b.WriteString("\\n")
			continue
		}
		if r == '\r' {
			b.WriteString("\\r")
			continue
		}
		if r == '\t' {
			b.WriteString("\\t")
			continue
		}
		if r >= 0x20 {
			b.WriteRune(r)
		}
		// otherwise skip control characters (including NUL)
	}
	return b.String()
}

var pendingPublishMap sync.Map

func sendSplitInteractionMessages(session *discordgo.Session, ic *discordgo.InteractionCreate, text string, ephemeral bool) error {
	runes := []rune(text)
	const maxLen = 1950

	var components *[]discordgo.MessageComponent
	if ephemeral {
		pubID := fmt.Sprintf("%d", time.Now().UnixNano())
		pendingPublishMap.Store(pubID, text)

		buttonRow := discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "전체에게 공개",
					Style:    discordgo.PrimaryButton,
					CustomID: "publish_ask:" + pubID,
				},
			},
		}
		components = &[]discordgo.MessageComponent{buttonRow}
	}

	if len(runes) <= maxLen {
		_, err := session.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{
			Content:    &text,
			Components: components,
		})
		return err
	}

	// Edit the first part
	firstPart := string(runes[:maxLen])
	_, err := session.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{
		Content: &firstPart,
	})
	if err != nil {
		return err
	}

	var flags discordgo.MessageFlags
	if ephemeral {
		flags = discordgo.MessageFlagsEphemeral
	}

	// Send remaining parts as followups
	remaining := runes[maxLen:]
	for len(remaining) > 0 {
		chunkLen := maxLen
		if len(remaining) < chunkLen {
			chunkLen = len(remaining)
		}
		chunk := string(remaining[:chunkLen])
		remaining = remaining[chunkLen:]

		params := &discordgo.WebhookParams{
			Content: chunk,
			Flags:   flags,
		}
		if len(remaining) == 0 && components != nil {
			params.Components = *components
		}

		_, err = session.FollowupMessageCreate(ic.Interaction, true, params)
		if err != nil {
			slog.Error("Failed to send followup message", "error", err)
			return err
		}
	}
	return nil
}

func sendSplitChannelMessages(session *discordgo.Session, channelID string, text string) error {
	runes := []rune(text)
	const maxLen = 1950
	if len(runes) <= maxLen {
		_, err := session.ChannelMessageSend(channelID, text)
		return err
	}

	remaining := runes
	for len(remaining) > 0 {
		chunkLen := maxLen
		if len(remaining) < chunkLen {
			chunkLen = len(remaining)
		}
		chunk := string(remaining[:chunkLen])
		_, err := session.ChannelMessageSend(channelID, chunk)
		if err != nil {
			slog.Error("Failed to send split channel message", "error", err)
			return err
		}
		remaining = remaining[chunkLen:]
	}
	return nil
}

func sendSplitFollowupMessages(session *discordgo.Session, ic *discordgo.InteractionCreate, text string) error {
	runes := []rune(text)
	const maxLen = 1950

	remaining := runes
	for len(remaining) > 0 {
		chunkLen := maxLen
		if len(remaining) < chunkLen {
			chunkLen = len(remaining)
		}
		chunk := string(remaining[:chunkLen])
		remaining = remaining[chunkLen:]

		_, err := session.FollowupMessageCreate(ic.Interaction, true, &discordgo.WebhookParams{
			Content: chunk,
			Flags:   0,
		})
		if err != nil {
			slog.Error("Failed to send public followup message", "error", err)
			return err
		}
	}
	return nil
}

func sendSplitEphemeralFollowup(session *discordgo.Session, ic *discordgo.InteractionCreate, text string) error {
	pubID := fmt.Sprintf("%d", time.Now().UnixNano())
	pendingPublishMap.Store(pubID, text)

	buttonRow := discordgo.ActionsRow{
		Components: []discordgo.MessageComponent{
			discordgo.Button{
				Label:    "전체에게 공개",
				Style:    discordgo.PrimaryButton,
				CustomID: "publish_ask:" + pubID,
			},
		},
	}
	components := []discordgo.MessageComponent{buttonRow}

	runes := []rune(text)
	const maxLen = 1950

	remaining := runes
	for len(remaining) > 0 {
		chunkLen := maxLen
		if len(remaining) < chunkLen {
			chunkLen = len(remaining)
		}
		chunk := string(remaining[:chunkLen])
		remaining = remaining[chunkLen:]

		params := &discordgo.WebhookParams{
			Content: chunk,
			Flags:   discordgo.MessageFlagsEphemeral,
		}
		if len(remaining) == 0 {
			params.Components = components
		}

		_, err := session.FollowupMessageCreate(ic.Interaction, true, params)
		if err != nil {
			slog.Error("Failed to send ephemeral followup message", "error", err)
			return err
		}
	}
	return nil
}
