package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

type WebhookEditLyricsV2 struct {
	Content     *string                         `json:"content,omitempty"`
	Components  *[]discordgo.MessageComponent   `json:"components,omitempty"`
	Embeds      *[]*discordgo.MessageEmbed      `json:"embeds,omitempty"`
	Flags       discordgo.MessageFlags          `json:"flags"`
	Attachments *[]*discordgo.MessageAttachment `json:"attachments,omitempty"`
}

func lyricsReplacementAttachments(files []*discordgo.File) []*discordgo.MessageAttachment {
	attachments := make([]*discordgo.MessageAttachment, 0, len(files))
	for i, file := range files {
		attachments = append(attachments, &discordgo.MessageAttachment{
			ID:       strconv.Itoa(i),
			Filename: file.Name,
		})
	}
	return attachments
}

func rewindLyricsFiles(files []*discordgo.File) error {
	for _, file := range files {
		seeker, ok := file.Reader.(io.Seeker)
		if !ok {
			return fmt.Errorf("lyrics file %q cannot be rewound", file.Name)
		}
		if _, err := seeker.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("rewind lyrics file %q: %w", file.Name, err)
		}
	}
	return nil
}

func lyricsPanelInteraction(existingMessageID string, inter *discordgo.Interaction) *discordgo.Interaction {
	if existingMessageID != "" {
		return nil
	}
	return inter
}

func buildLyricsComponentsV2(
	title string,
	subTitle string,
	containerText string,
	footerText string,
	imageMode bool,
	hasImageFile bool,
	buttons []discordgo.MessageComponent,
) []discordgo.MessageComponent {
	builder := NewComponentsBuilder().
		WithTitle(title).
		WithSubTitle(subTitle).
		WithFooter(footerText).
		WithButtons(buttons...)

	if imageMode && hasImageFile {
		builder.WithImage("attachment://lyrics.jpg")
	} else if containerText != "" {
		builder.WithBody(containerText)
	}

	return builder.Build()
}

func editLyricsMessage(
	s *discordgo.Session,
	ic *discordgo.InteractionCreate,
	pbSession *LyricsPlaybackSession,
	title string,
	subTitle string,
	containerText string,
	footerText string,
	buttons []discordgo.MessageComponent,
) (*discordgo.Message, error) {
	if pbSession != nil {
		pbSession.editMu.Lock()
		defer pbSession.editMu.Unlock()
	}

	var channelID string
	var messageID string
	var inter *discordgo.Interaction
	var imageMode bool
	var framesDir string
	var lastIdx int

	if pbSession != nil {
		pbSession.mu.RLock()
		channelID = pbSession.ChannelID
		messageID = pbSession.MessageID
		inter = pbSession.Interaction
		imageMode = pbSession.ImageMode
		framesDir = pbSession.FramesDir
		lastIdx = pbSession.LastIdx
		pbSession.mu.RUnlock()
	}

	if inter == nil && ic != nil && messageID == "" {
		inter = ic.Interaction
	}

	var files []*discordgo.File
	var fileCloser io.Closer
	if imageMode && framesDir != "" && lastIdx >= 0 {
		framePath := fmt.Sprintf("%s/frame_%03d.jpg", framesDir, lastIdx)
		if f, err := os.Open(framePath); err == nil {
			files = []*discordgo.File{
				{
					Name:        "lyrics.jpg",
					ContentType: "image/jpeg",
					Reader:      f,
				},
			}
			fileCloser = f
		} else {
			slog.Warn("Could not open lyrics frame file", "path", framePath, "error", err)
		}
	}
	if fileCloser != nil {
		defer fileCloser.Close()
	}

	allComponents := buildLyricsComponentsV2(title, subTitle, containerText, footerText, imageMode, len(files) > 0, buttons)

	appID := ""
	token := ""
	if inter != nil {
		appID = inter.AppID
		token = inter.Token
	}
	if appID == "" && s.State != nil && s.State.User != nil {
		appID = s.State.User.ID
	}

	var msg *discordgo.Message
	var err error
	replacementAttachments := lyricsReplacementAttachments(files)

	// 1. Primary: Edit via Interaction Webhook
	if token != "" && appID != "" {
		uri := discordgo.EndpointWebhookMessage(appID, token, "@original")
		editV2 := WebhookEditLyricsV2{
			Components:  &allComponents,
			Flags:       discordgo.MessageFlagsIsComponentsV2,
			Attachments: &replacementAttachments,
		}
		if len(files) > 0 {
			contentType, body, encodeErr := discordgo.MultipartBodyWithJSON(editV2, files)
			if encodeErr == nil {
				response, reqErr := s.RequestRaw("PATCH", uri, contentType, body, uri, 0)
				if reqErr == nil && len(response) > 0 {
					_ = json.Unmarshal(response, &msg)
					if msg != nil && pbSession != nil {
						pbSession.mu.Lock()
						if pbSession.MessageID == "" {
							pbSession.MessageID = msg.ID
						}
						if pbSession.ChannelID == "" {
							pbSession.ChannelID = msg.ChannelID
						}
						pbSession.mu.Unlock()
						return msg, nil
					}
				} else {
					err = reqErr
					slog.Warn("Webhook Multipart PATCH failed", "error", reqErr, "response", string(response))
				}
			} else {
				err = encodeErr
				slog.Warn("Webhook Multipart encode failed", "error", encodeErr)
			}
		} else {
			response, reqErr := s.RequestWithBucketID("PATCH", uri, editV2, discordgo.EndpointWebhookToken("", ""))
			if reqErr == nil && len(response) > 0 {
				_ = json.Unmarshal(response, &msg)
				if msg != nil && pbSession != nil {
					pbSession.mu.Lock()
					if pbSession.MessageID == "" {
						pbSession.MessageID = msg.ID
					}
					if pbSession.ChannelID == "" {
						pbSession.ChannelID = msg.ChannelID
					}
					pbSession.mu.Unlock()
					return msg, nil
				}
			} else {
				err = reqErr
				slog.Warn("Webhook JSON PATCH failed", "error", reqErr, "response", string(response))
			}
		}
	}

	if msg == nil && inter != nil && messageID == "" {
		if rewindErr := rewindLyricsFiles(files); rewindErr != nil {
			return nil, rewindErr
		}
		msg, err = s.InteractionResponseEdit(inter, &discordgo.WebhookEdit{
			Components:  &allComponents,
			Files:       files,
			Attachments: &replacementAttachments,
		})
		if msg != nil && pbSession != nil {
			pbSession.mu.Lock()
			pbSession.MessageID = msg.ID
			if pbSession.ChannelID == "" {
				pbSession.ChannelID = msg.ChannelID
			}
			pbSession.mu.Unlock()
			return msg, nil
		}
	}

	// 2. Fallback: Edit via permanent Bot Token
	if channelID != "" && messageID != "" {
		if rewindErr := rewindLyricsFiles(files); rewindErr != nil {
			return nil, rewindErr
		}
		edit := &discordgo.MessageEdit{
			Channel:     channelID,
			ID:          messageID,
			Components:  &allComponents,
			Flags:       discordgo.MessageFlagsIsComponentsV2,
			Files:       files,
			Attachments: &replacementAttachments,
		}
		var botErr error
		msg, botErr = s.ChannelMessageEditComplex(edit)
		if botErr == nil && msg != nil {
			return msg, nil
		}
		if botErr != nil {
			err = botErr
			slog.Warn("Channel lyrics PATCH failed", "message_id", messageID, "channel_id", channelID, "error", botErr)
		}
	}

	if err != nil {
		slog.Warn("Failed to edit lyrics message", "message_id", messageID, "channel_id", channelID, "error", err)
	}
	return msg, err
}

func deleteLyricsMessage(
	s *discordgo.Session,
	ic *discordgo.InteractionCreate,
	pbSession *LyricsPlaybackSession,
) error {
	var targetMessages []struct{ channelID, messageID string }
	var inter *discordgo.Interaction

	if ic != nil {
		inter = ic.Interaction
		if ic.Message != nil && ic.Message.ID != "" {
			targetMessages = append(targetMessages, struct{ channelID, messageID string }{
				channelID: ic.Message.ChannelID,
				messageID: ic.Message.ID,
			})
		}
	}

	if pbSession != nil {
		pbSession.mu.RLock()
		sessChan := pbSession.ChannelID
		sessMsg := pbSession.MessageID
		if inter == nil {
			inter = pbSession.Interaction
		}
		pbSession.mu.RUnlock()

		if sessChan != "" && sessMsg != "" {
			alreadyAdded := false
			for _, m := range targetMessages {
				if m.messageID == sessMsg {
					alreadyAdded = true
					break
				}
			}
			if !alreadyAdded {
				targetMessages = append(targetMessages, struct{ channelID, messageID string }{
					channelID: sessChan,
					messageID: sessMsg,
				})
			}
		}
	}

	var lastErr error
	for _, m := range targetMessages {
		if err := s.ChannelMessageDelete(m.channelID, m.messageID); err != nil {
			lastErr = err
		}
	}

	if inter != nil {
		if err := s.InteractionResponseDelete(inter); err != nil {
			lastErr = err
		}
	}

	return lastErr
}

func buildLyricsDescription(result *LyricResult, currentIdx int) string {
	result.mu.RLock()
	defer result.mu.RUnlock()

	lines := result.Lines
	var parts []string

	start := currentIdx - LyricsContextLines
	if start < 0 {
		start = 0
	}
	for i := start; i < currentIdx; i++ {
		parts = append(parts, fmt.Sprintf("-# %s", lines[i].Text))
	}

	current := lines[currentIdx]
	parts = append(parts, fmt.Sprintf("> **%s**", current.Text))

	if currentIdx < len(result.TranslatedLines) && strings.TrimSpace(result.TranslatedLines[currentIdx]) != "" {
		parts = append(parts, fmt.Sprintf("> 🌐 %s", result.TranslatedLines[currentIdx]))
	}

	if currentIdx < len(result.PronunciationLines) && strings.TrimSpace(result.PronunciationLines[currentIdx]) != "" {
		parts = append(parts, fmt.Sprintf("> 🇰🇷 %s", result.PronunciationLines[currentIdx]))
	}

	end := currentIdx + LyricsContextLines
	if end >= len(lines) {
		end = len(lines) - 1
	}
	for i := currentIdx + 1; i <= end; i++ {
		parts = append(parts, fmt.Sprintf("-# %s", lines[i].Text))
	}

	desc := strings.Join(parts, "\n")
	if len(desc) > LyricsMaxDescLen {
		var reduced []string
		reduced = append(reduced, fmt.Sprintf("> **%s**", current.Text))
		if currentIdx < len(result.TranslatedLines) && result.TranslatedLines[currentIdx] != "" {
			reduced = append(reduced, fmt.Sprintf("> 🌐 %s", result.TranslatedLines[currentIdx]))
		}
		if currentIdx < len(result.PronunciationLines) && result.PronunciationLines[currentIdx] != "" {
			reduced = append(reduced, fmt.Sprintf("> 🇰🇷 %s", result.PronunciationLines[currentIdx]))
		}
		desc = strings.Join(reduced, "\n")
	}

	return desc
}

func buildLyricsFooter(result *LyricResult, elapsedMs int) string {
	if result == nil {
		return ""
	}
	sourceLabel := "LRCLIB"
	if result.Source == "spotify" {
		sourceLabel = "Spotify"
	} else if result.Source == "petitlyrics" {
		sourceLabel = "PetitLyrics"
	}

	result.mu.RLock()
	modelLabel := result.TranslationModel
	rawLang := strings.ToUpper(strings.TrimSpace(result.DetectedLang))
	result.mu.RUnlock()

	if modelLabel == "원문" {
		return fmt.Sprintf("소스: %s • 원문 (%s) | %s / %s", sourceLabel, rawLang, fmtMs(elapsedMs), fmtMs(result.DurationMs))
	} else if modelLabel != "" {
		if rawLang != "" {
			return fmt.Sprintf("소스: %s • 번역: %s (%s) | %s / %s", sourceLabel, modelLabel, rawLang, fmtMs(elapsedMs), fmtMs(result.DurationMs))
		}
		return fmt.Sprintf("소스: %s • 번역: %s | %s / %s", sourceLabel, modelLabel, fmtMs(elapsedMs), fmtMs(result.DurationMs))
	}
	return fmt.Sprintf("소스: %s • 🔄 번역 중... | %s / %s", sourceLabel, fmtMs(elapsedMs), fmtMs(result.DurationMs))
}

func buildLyricsButtons(playbackKey string, hasLyrics bool, disabled bool, showFuzzy bool, isAuto bool, isDedicated bool, queueLen int) []discordgo.MessageComponent {
	var buttons []discordgo.MessageComponent

	if hasLyrics {
		buttons = append(buttons, discordgo.Button{
			Label:    "⏪ -1초",
			Style:    discordgo.SecondaryButton,
			CustomID: "lyrics_offset_minus:" + playbackKey,
			Disabled: disabled,
		})
	} else if showFuzzy {
		buttons = append(buttons, discordgo.Button{
			Label:    "🔍 유사 검색",
			Style:    discordgo.SecondaryButton,
			CustomID: "lyrics_fuzzy:" + playbackKey,
			Disabled: disabled,
		})
	}

	// Refresh or Skip: Not needed on dedicated pinned channel
	if !isDedicated {
		if queueLen > 0 && hasLyrics && !isAuto {
			buttons = append(buttons, discordgo.Button{
				Label:    "⏭️ 스킵",
				Style:    discordgo.PrimaryButton,
				CustomID: "lyrics_skip:" + playbackKey,
				Disabled: disabled,
			})
		} else {
			buttons = append(buttons, discordgo.Button{
				Label:    "🔄 새로고침",
				Style:    discordgo.PrimaryButton,
				CustomID: "lyrics_refresh:" + playbackKey,
				Disabled: disabled,
			})
		}
	}

	if hasLyrics {
		buttons = append(buttons, discordgo.Button{
			Label:    "⏩ +1초",
			Style:    discordgo.SecondaryButton,
			CustomID: "lyrics_offset_plus:" + playbackKey,
			Disabled: disabled,
		})
	}

	// Queue button: Only for manual lyrics search / playback mode
	if !isAuto && !isDedicated {
		queueLabel := "➕ 곡 추가"
		if queueLen > 0 {
			queueLabel = fmt.Sprintf("➕ 대기 (%d)", queueLen)
		}
		buttons = append(buttons, discordgo.Button{
			Label:    queueLabel,
			Style:    discordgo.SecondaryButton,
			CustomID: "lyrics_queue_add:" + playbackKey,
			Disabled: disabled,
		})
	}

	stopLabel := "⏹️ 중지"
	if isDedicated {
		stopLabel = "⏹️ 고정 해제"
	}
	buttons = append(buttons, discordgo.Button{
		Label:    stopLabel,
		Style:    discordgo.DangerButton,
		CustomID: "lyrics_stop:" + playbackKey,
		Disabled: disabled,
	})

	return buttons
}

func buildLyricsCommand() (BotCommand, error) {
	return NewBotCommandBuilder("lyrics").
		WithDescription(".").
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionSubCommand,
			Name:        "search",
			Description: ".",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "query",
					Description: ".",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionBoolean,
					Name:        "image",
					Description: ".",
					Required:    false,
				},
			},
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionSubCommand,
			Name:        "spotify",
			Description: ".",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: ".",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionBoolean,
					Name:        "image",
					Description: ".",
					Required:    false,
				},
			},
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionSubCommand,
			Name:        "pin",
			Description: ".",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: ".",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionBoolean,
					Name:        "image",
					Description: ".",
					Required:    false,
				},
			},
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionSubCommand,
			Name:        "unpin",
			Description: ".",
		}).
		WithFunction(handleLyricsCommand).
		Build()
}

func handleLyricsCommand(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	subcmd := "search"
	var subOptions []*discordgo.ApplicationCommandInteractionDataOption

	data := ic.ApplicationCommandData()
	if len(data.Options) > 0 {
		firstOpt := data.Options[0]
		if firstOpt.Type == discordgo.ApplicationCommandOptionSubCommand {
			subcmd = firstOpt.Name
			subOptions = firstOpt.Options
		} else {
			subOptions = data.Options
		}
	}

	if subcmd == "spotify" {
		targetUserID := getUserID(ic)
		targetUsername := getUserName(ic)
		var imageMode bool

		for _, opt := range subOptions {
			if opt.Name == "user" && opt.Value != nil {
				if u := opt.UserValue(s); u != nil {
					targetUserID = u.ID
					targetUsername = u.Username
					if u.GlobalName != "" {
						targetUsername = u.GlobalName
					}
				}
			} else if opt.Name == "image" && opt.Value != nil {
				imageMode = opt.BoolValue()
			}
		}
		handleSpotifyAuto(s, ic, targetUserID, targetUsername, imageMode)
		return
	}

	if subcmd == "pin" {
		targetUserID := getUserID(ic)
		targetUsername := getUserName(ic)
		imageMode := true

		for _, opt := range subOptions {
			if opt.Name == "user" && opt.Value != nil {
				if u := opt.UserValue(s); u != nil {
					targetUserID = u.ID
					targetUsername = u.Username
					if u.GlobalName != "" {
						targetUsername = u.GlobalName
					}
				}
			} else if opt.Name == "image" && opt.Value != nil {
				imageMode = opt.BoolValue()
			}
		}
		handleSpotifyPin(s, ic, targetUserID, targetUsername, imageMode)
		return
	}

	if subcmd == "unpin" {
		handleSpotifyUnpin(s, ic)
		return
	}

	var query string
	var imageMode bool
	for _, opt := range subOptions {
		if opt.Name == "query" && opt.Value != nil {
			query = strings.TrimSpace(opt.StringValue())
		} else if opt.Name == "image" && opt.Value != nil {
			imageMode = opt.BoolValue()
		}
	}
	if query == "" {
		query = strings.TrimSpace(getInteractionOptionString(ic, "query"))
	}

	handleLyricsSearch(s, ic, query, imageMode)
}

func handleLyricsSearch(s *discordgo.Session, ic *discordgo.InteractionCreate, query string, imageMode bool) {
	cmdStartTime := time.Now()
	if query == "" {
		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "⚠️ 검색할 곡 제목 또는 링크를 입력해 주세요.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	userID := getUserID(ic)

	var trackTitle, artistName, coverURL string
	var trackDuration int
	searchQuery := query

	if spMatch := spotifyTrackRegex.FindStringSubmatch(query); len(spMatch) > 1 {
		trackID := spMatch[1]
		t, a, c, d := fetchSpotifyTrackInfo(trackID)
		if t != "" {
			trackTitle = t
			artistName = a
			searchQuery = t + " " + a
		}
		if c != "" {
			coverURL = c
		}
		if d > 0 {
			trackDuration = d
		}
	} else if ytMatch := ytVideoRegex.FindStringSubmatch(query); len(ytMatch) > 1 {
		videoID := ytMatch[1]
		t, c := fetchYouTubeTitleAndCover(videoID)
		if t != "" {
			searchQuery = t
		}
		if c != "" {
			coverURL = c
		}
	}

	result, err := fetchLyricsConcurrent(searchQuery, trackTitle, artistName, false)
	if err != nil {
		slog.Warn("Failed to fetch lyrics", "query", query, "track", trackTitle, "artist", artistName, "error", err)
		playbackKey := fmt.Sprintf("%d", time.Now().UnixNano())
		pbSession := &LyricsPlaybackSession{
			Key:         playbackKey,
			OwnerID:     userID,
			ChannelID:   ic.ChannelID,
			SearchQuery: searchQuery,
			TrackTitle:  trackTitle,
			ArtistName:  artistName,
			CoverURL:    coverURL,
			ImageMode:   imageMode,
			FramesDir:   fmt.Sprintf("/tmp/lyrics_frames_%s", playbackKey),
			Done:        make(chan struct{}),
			Interaction: ic.Interaction,
		}
		activeLyricsPlaybacks.Store(playbackKey, pbSession)
		channelLyricsPlaybacks.Store(ic.ChannelID, pbSession)

		if imageMode {
			_ = os.MkdirAll(pbSession.FramesDir, 0755)
			nfPath := fmt.Sprintf("%s/frame_000.jpg", pbSession.FramesDir)
			_ = RenderNotFoundLyricsFrame(coverURL, nfPath)
			pbSession.LastIdx = 0
		}
		title := trackTitle
		if title == "" {
			title = query
		}
		buttons := buildLyricsButtons(playbackKey, false, false, true, false, false, pbSession.QueueLength())
		_, _ = editLyricsMessage(s, ic, pbSession, title, artistName, "❌ **가사를 찾을 수 없습니다.**\n\n🔍 **유사 검색을 시도하려면 아래 버튼을 눌러주세요.**", "가사 검색 결과 없음", buttons)
		return
	}

	if coverURL != "" {
		result.CoverURL = coverURL
	}
	if trackDuration > 0 && result.DurationMs <= 0 {
		result.DurationMs = trackDuration
	}

	elapsedOnStart := int(time.Since(cmdStartTime).Milliseconds())
	pbSession := startLyricsLivePlayback(s, ic, userID, result, elapsedOnStart, cmdStartTime, imageMode)

	geminiKey := ""
	openAIKey := ""
	if botConfig != nil {
		geminiKey = botConfig.GeminiAPIKey
		openAIKey = botConfig.OpenAIAPIKey
	}
	if (geminiKey != "" || openAIKey != "") && len(result.Lines) > 0 {
		go func() {
			attachLyricsTranslation(result, geminiKey, openAIKey)
			if pbSession != nil {
				if pbSession.ImageMode {
					pbSession.PreRenderLyricsFramesSync()
				}
				pbSession.mu.Lock()
				pbSession.LastIdx = -1
				pbSession.mu.Unlock()
				pbSession.TriggerImmediateUpdate(s, ic)
			}
		}()
	}
}

func handleSpotifyAuto(s *discordgo.Session, ic *discordgo.InteractionCreate, targetUserID, targetUsername string, imageMode bool) {
	channelID := ic.ChannelID
	StopAndClearChannelPlayback(channelID)

	var existingMsgID string
	var isDedicated bool
	if redisClient != nil {
		if st, err := redisClient.GetChannelSpotifyState(channelID); err == nil && st != nil && st.MessageID != "" {
			if m, err := s.ChannelMessage(channelID, st.MessageID); err == nil && m != nil {
				existingMsgID = st.MessageID
				isDedicated = st.IsDedicated
			}
		}
	}

	playbackKey := fmt.Sprintf("%d", time.Now().UnixNano())
	pbSession := &LyricsPlaybackSession{
		Key:         playbackKey,
		OwnerID:     getUserID(ic),
		ChannelID:   channelID,
		MessageID:   existingMsgID,
		Done:        make(chan struct{}),
		IsAuto:      true,
		IsDedicated: isDedicated,
		ImageMode:   imageMode,
		FramesDir:   fmt.Sprintf("/tmp/lyrics_frames_%s", playbackKey),
		Interaction: lyricsPanelInteraction(existingMsgID, ic.Interaction),
	}

	activeLyricsPlaybacks.Store(playbackKey, pbSession)
	channelLyricsPlaybacks.Store(channelID, pbSession)

	if existingMsgID != "" {
		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "🎧 기존 가사 패널에 연결하여 실시간 추적을 시작합니다.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
	} else {
		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		})
	}

	initialAct := getSpotifyActivity(s, targetUserID, ic.GuildID)
	if initialAct == nil {
		buttons := buildLyricsButtons(playbackKey, false, false, false, true, isDedicated, pbSession.QueueLength())
		title := fmt.Sprintf("🎧 %s님의 Spotify 실시간 가사 추적 시작", targetUsername)
		desc := "Spotify에서 노래를 재생하면 자동으로 가사와 실시간 번역이 시작됩니다...\n\n⏹️ **종료하려면 아래 중지 버튼을 눌러주세요.**"
		footer := "자동 추적 모드 활성화됨"
		if isDedicated {
			footer = "상시 고정 전용 채널 • 24/7 유지"
		}
		msg, err := editLyricsMessage(s, ic, pbSession, title, "", desc, footer, buttons)
		if err != nil {
			slog.Error("Failed to send initial auto lyrics", "error", err)
			activeLyricsPlaybacks.Delete(playbackKey)
			channelLyricsPlaybacks.Delete(channelID)
			return
		}
		if msg != nil {
			pbSession.mu.Lock()
			pbSession.MessageID = msg.ID
			pbSession.mu.Unlock()
		}
	}

	if redisClient != nil {
		_ = redisClient.SaveChannelSpotifyState(&ChannelSpotifyState{
			ChannelID:      channelID,
			GuildID:        ic.GuildID,
			MessageID:      pbSession.MessageID,
			PlaybackKey:    playbackKey,
			TargetUserID:   targetUserID,
			TargetUsername: targetUsername,
			OwnerID:        pbSession.OwnerID,
			IsAuto:         true,
			IsDedicated:    isDedicated,
			ImageMode:      imageMode,
		})
	}

	go runSpotifyAutoTrackingLoop(s, pbSession, targetUserID, targetUsername, ic.GuildID)
}

func handleSpotifyPin(s *discordgo.Session, ic *discordgo.InteractionCreate, targetUserID, targetUsername string, imageMode bool) {
	channelID := ic.ChannelID
	StopAndClearChannelPlayback(channelID)

	var existingMsgID string
	if redisClient != nil {
		if st, err := redisClient.GetChannelSpotifyState(channelID); err == nil && st != nil && st.MessageID != "" {
			if m, err := s.ChannelMessage(channelID, st.MessageID); err == nil && m != nil {
				existingMsgID = st.MessageID
			}
		}
	}

	playbackKey := fmt.Sprintf("%d", time.Now().UnixNano())
	pbSession := &LyricsPlaybackSession{
		Key:         playbackKey,
		OwnerID:     getUserID(ic),
		ChannelID:   channelID,
		MessageID:   existingMsgID,
		Done:        make(chan struct{}),
		IsAuto:      true,
		IsDedicated: true,
		ImageMode:   imageMode,
		FramesDir:   fmt.Sprintf("/tmp/lyrics_frames_%s", playbackKey),
		Interaction: lyricsPanelInteraction(existingMsgID, ic.Interaction),
	}

	activeLyricsPlaybacks.Store(playbackKey, pbSession)
	channelLyricsPlaybacks.Store(channelID, pbSession)

	if existingMsgID != "" {
		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "📌 현재 채널의 기존 가사 패널을 상시 고정 전용 패널(24/7)로 등록했습니다.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
	} else {
		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		})
	}

	initialAct := getSpotifyActivity(s, targetUserID, ic.GuildID)
	if initialAct == nil {
		buttons := buildLyricsButtons(playbackKey, false, false, false, true, true, pbSession.QueueLength())
		title := fmt.Sprintf("📌 %s님의 Spotify 상시 고정 가사 패널", targetUsername)
		desc := "Spotify에서 노래를 재생하면 이 패널에서 자동으로 가사와 실시간 번역이 시작됩니다...\n\n⏹️ **고정 해제하려면 아래 중지 버튼을 누르거나 `/lyrics unpin`을 실행하세요.**"
		footer := "상시 고정 전용 채널 활성화됨 • 24/7 유지"
		msg, err := editLyricsMessage(s, ic, pbSession, title, "", desc, footer, buttons)
		if err != nil {
			slog.Error("Failed to send initial pinned auto lyrics", "error", err)
			activeLyricsPlaybacks.Delete(playbackKey)
			channelLyricsPlaybacks.Delete(channelID)
			return
		}
		if msg != nil {
			pbSession.mu.Lock()
			pbSession.MessageID = msg.ID
			pbSession.mu.Unlock()
		}
	}

	if redisClient != nil {
		_ = redisClient.SaveChannelSpotifyState(&ChannelSpotifyState{
			ChannelID:      channelID,
			GuildID:        ic.GuildID,
			MessageID:      pbSession.MessageID,
			PlaybackKey:    playbackKey,
			TargetUserID:   targetUserID,
			TargetUsername: targetUsername,
			OwnerID:        pbSession.OwnerID,
			IsAuto:         true,
			IsDedicated:    true,
			ImageMode:      imageMode,
		})
	}

	go runSpotifyAutoTrackingLoop(s, pbSession, targetUserID, targetUsername, ic.GuildID)
}

func handleSpotifyUnpin(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	channelID := ic.ChannelID
	StopAndClearChannelPlayback(channelID)
	if redisClient != nil {
		_ = redisClient.DeleteChannelSpotifyState(channelID)
	}
	_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "🔓 현재 채널의 Spotify 상시 고정 가사 패널 설정이 해제되었습니다.",
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

func startLyricsLivePlayback(
	s *discordgo.Session,
	ic *discordgo.InteractionCreate,
	userID string,
	result *LyricResult,
	initialElapsedMs int,
	sessionStartTime time.Time,
	imageMode bool,
) *LyricsPlaybackSession {
	channelID := ic.ChannelID
	StopAndClearChannelPlayback(channelID)

	var existingMsgID string
	if redisClient != nil {
		if st, err := redisClient.GetChannelSpotifyState(channelID); err == nil && st != nil && st.MessageID != "" {
			if m, err := s.ChannelMessage(channelID, st.MessageID); err == nil && m != nil {
				existingMsgID = st.MessageID
			}
		}
	}

	initialIdx := findCurrentLyricsIndex(result.Lines, initialElapsedMs)
	playbackKey := fmt.Sprintf("%d", time.Now().UnixNano())
	pbSession := &LyricsPlaybackSession{
		Key:         playbackKey,
		OwnerID:     userID,
		ChannelID:   channelID,
		MessageID:   existingMsgID,
		Result:      result,
		StartTime:   sessionStartTime,
		LastIdx:     initialIdx,
		ImageMode:   imageMode,
		FramesDir:   fmt.Sprintf("/tmp/lyrics_frames_%s", playbackKey),
		Done:        make(chan struct{}),
		Interaction: ic.Interaction,
	}

	activeLyricsPlaybacks.Store(playbackKey, pbSession)
	channelLyricsPlaybacks.Store(channelID, pbSession)

	if imageMode && len(result.Lines) > 0 {
		pbSession.PreRenderLyricsFramesSync()
	}

	buttons := buildLyricsButtons(playbackKey, true, false, false, pbSession.IsAuto, pbSession.IsDedicated, pbSession.QueueLength())
	desc := buildLyricsDescription(result, initialIdx)
	footer := buildLyricsFooter(result, initialElapsedMs)
	msg, editErr := editLyricsMessage(s, ic, pbSession, result.TrackName, result.Artist, desc, footer, buttons)
	if editErr != nil {
		slog.Error("Failed to send initial lyrics", "error", editErr)
		activeLyricsPlaybacks.Delete(playbackKey)
		channelLyricsPlaybacks.Delete(channelID)
		if pbSession.FramesDir != "" {
			_ = os.RemoveAll(pbSession.FramesDir)
		}
		return nil
	}
	if msg != nil {
		pbSession.mu.Lock()
		pbSession.MessageID = msg.ID
		pbSession.mu.Unlock()
	}

	if redisClient != nil {
		_ = redisClient.SaveChannelSpotifyState(&ChannelSpotifyState{
			ChannelID:      channelID,
			GuildID:        ic.GuildID,
			MessageID:      pbSession.MessageID,
			PlaybackKey:    playbackKey,
			TargetUserID:   userID,
			TargetUsername: "",
			OwnerID:        userID,
			IsAuto:         false,
			ImageMode:      imageMode,
		})
	}

	go runLyricsPlaybackLoop(s, ic, pbSession)
	return pbSession
}

func preloadQueuedLyricsItem(pbSession *LyricsPlaybackSession, item *QueuedLyricsItem) {
	defer close(item.ReadyChan)

	query := item.Query
	var trackTitle, artistName, coverURL string
	var trackDuration int
	searchQuery := query

	if spMatch := spotifyTrackRegex.FindStringSubmatch(query); len(spMatch) > 1 {
		trackID := spMatch[1]
		t, a, c, d := fetchSpotifyTrackInfo(trackID)
		if t != "" {
			trackTitle = t
			artistName = a
			searchQuery = t + " " + a
		}
		if c != "" {
			coverURL = c
		}
		if d > 0 {
			trackDuration = d
		}
	} else if ytMatch := ytVideoRegex.FindStringSubmatch(query); len(ytMatch) > 1 {
		videoID := ytMatch[1]
		t, c := fetchYouTubeTitleAndCover(videoID)
		if t != "" {
			searchQuery = t
		}
		if c != "" {
			coverURL = c
		}
	}

	res, err := fetchLyricsConcurrent(searchQuery, trackTitle, artistName, false)
	if err != nil {
		res, err = fetchLyricsConcurrent(searchQuery, trackTitle, artistName, true)
	}

	if err != nil || res == nil || len(res.Lines) == 0 {
		item.Err = fmt.Errorf("가사를 찾을 수 없습니다: %w", err)
		return
	}

	if coverURL != "" && res.CoverURL == "" {
		res.CoverURL = coverURL
	}
	if trackDuration > 0 && res.DurationMs <= 0 {
		res.DurationMs = trackDuration
	}

	geminiKey := ""
	openAIKey := ""
	if botConfig != nil {
		geminiKey = botConfig.GeminiAPIKey
		openAIKey = botConfig.OpenAIAPIKey
	}
	if (geminiKey != "" || openAIKey != "") && len(res.Lines) > 0 {
		attachLyricsTranslation(res, geminiKey, openAIKey)
	}

	if pbSession.ImageMode && len(res.Lines) > 0 {
		framesDir := fmt.Sprintf("/tmp/lyrics_frames_%s_q%d", pbSession.Key, time.Now().UnixNano())
		_ = os.MkdirAll(framesDir, 0755)

		res.mu.RLock()
		renderLines := make([]RenderLyricLine, len(res.Lines))
		for i, l := range res.Lines {
			var trans, pron string
			if i < len(res.TranslatedLines) {
				trans = res.TranslatedLines[i]
			}
			if i < len(res.PronunciationLines) {
				pron = res.PronunciationLines[i]
			}
			renderLines[i] = RenderLyricLine{
				Text:     l.Text,
				Trans:    trans,
				Phonetic: pron,
				TimeMs:   l.TimeMs,
			}
		}
		coverURL := res.CoverURL
		durMs := res.DurationMs
		res.mu.RUnlock()

		err = RenderLyricsFramesNative(coverURL, durMs, renderLines, framesDir)
		if err == nil {
			item.FramesDir = framesDir
		} else {
			slog.Warn("Failed to pre-render queued lyrics frames", "error", err)
		}
	}

	item.Result = res
}

func playQueuedSong(s *discordgo.Session, ic *discordgo.InteractionCreate, pbSession *LyricsPlaybackSession, item *QueuedLyricsItem) {
	playbackKey := pbSession.Key
	query := item.Query

	pbSession.mu.Lock()
	pbSession.SearchQuery = query
	pbSession.LastIdx = -1
	pbSession.mu.Unlock()

	// If the queued item is still being fetched/translated, wait on ReadyChan
	select {
	case <-item.ReadyChan:
	default:
		busyButtons := buildLyricsButtons(playbackKey, false, true, false, pbSession.IsAuto, pbSession.IsDedicated, pbSession.QueueLength())
		_, _ = editLyricsMessage(s, ic, pbSession, query, "", "⏳ **대기열 곡 가사와 번역을 준비하는 중입니다...**", "대기열 곡 준비 중", busyButtons)
		select {
		case <-item.ReadyChan:
		case <-time.After(12 * time.Second):
		}
	}

	res := item.Result
	if res == nil || len(res.Lines) == 0 {
		slog.Warn("Failed to fetch lyrics for queued song", "query", query, "error", item.Err)
		pbSession.mu.Lock()
		pbSession.Result = nil
		pbSession.LastIdx = -1
		pbSession.mu.Unlock()

		if pbSession.ImageMode {
			if pbSession.FramesDir == "" {
				pbSession.FramesDir = fmt.Sprintf("/tmp/lyrics_frames_%s", playbackKey)
			}
			_ = os.MkdirAll(pbSession.FramesDir, 0755)
			nfPath := fmt.Sprintf("%s/frame_000.jpg", pbSession.FramesDir)
			_ = RenderNotFoundLyricsFrame("", nfPath)
			pbSession.mu.Lock()
			pbSession.LastIdx = 0
			pbSession.mu.Unlock()
		}

		buttons := buildLyricsButtons(playbackKey, false, false, true, pbSession.IsAuto, pbSession.IsDedicated, pbSession.QueueLength())
		_, _ = editLyricsMessage(s, ic, pbSession, query, "", fmt.Sprintf("❌ **[%s] 가사를 찾을 수 없습니다.**", query), "대기열 곡 검색 실패", buttons)
		return
	}

	pbSession.mu.Lock()
	pbSession.Result = res
	pbSession.StartTime = time.Now()
	pbSession.Offset = 0
	pbSession.LastIdx = -1
	pbSession.TrackTitle = res.TrackName
	pbSession.ArtistName = res.Artist
	pbSession.CoverURL = res.CoverURL
	if item.FramesDir != "" {
		if pbSession.FramesDir != "" && pbSession.FramesDir != item.FramesDir {
			_ = os.RemoveAll(pbSession.FramesDir)
		}
		pbSession.FramesDir = item.FramesDir
	}
	pbSession.mu.Unlock()

	if pbSession.ImageMode && item.FramesDir == "" && len(res.Lines) > 0 {
		pbSession.PreRenderLyricsFramesSync()
	}

	buttons := buildLyricsButtons(playbackKey, true, false, false, pbSession.IsAuto, pbSession.IsDedicated, pbSession.QueueLength())
	desc := buildLyricsDescription(res, 0)
	footer := buildLyricsFooter(res, 0)
	_, _ = editLyricsMessage(s, ic, pbSession, res.TrackName, res.Artist, desc, footer, buttons)

	go runLyricsPlaybackLoop(s, ic, pbSession)
}

func runLyricsPlaybackLoop(s *discordgo.Session, ic *discordgo.InteractionCreate, pbSession *LyricsPlaybackSession) {
	playbackKey := pbSession.Key
	channelID := pbSession.ChannelID

	defer func() {
		activeLyricsPlaybacks.Delete(playbackKey)
		channelLyricsPlaybacks.Delete(channelID)
		if pbSession.FramesDir != "" {
			_ = os.RemoveAll(pbSession.FramesDir)
			slog.Info("Cleaned up lyrics frames directory", "dir", pbSession.FramesDir)
		}
	}()

	ticker := time.NewTicker(LyricsUpdateInterval)
	defer ticker.Stop()

	var busy bool

	for {
		select {
		case <-pbSession.Done:
			return
		case <-ticker.C:
			if pbSession.Stopped.Load() {
				return
			}

			pbSession.mu.RLock()
			result := pbSession.Result
			pbSession.mu.RUnlock()

			if result == nil || len(result.Lines) == 0 {
				continue
			}

			// Periodic Spotify Calibration if owner is playing this track on Spotify
			if pbSession.OwnerID != "" && result != nil {
				spAct := getSpotifyActivity(s, pbSession.OwnerID, channelID)
				if spAct != nil && spAct.Timestamps.StartTimestamp > 0 {
					CalibrateWithSpotifyActivity(pbSession, spAct, result.TrackName, result.Artist)
					if spAct.Timestamps.EndTimestamp > spAct.Timestamps.StartTimestamp {
						spDuration := int(spAct.Timestamps.EndTimestamp - spAct.Timestamps.StartTimestamp)
						if result.DurationMs <= 0 || (spDuration > 0 && absInt(result.DurationMs-spDuration) > 4000) {
							result.DurationMs = spDuration
						}
					}
				}
			}

			totalMs := result.DurationMs
			elapsed := pbSession.GetElapsedMs()
			currentIdx := findCurrentLyricsIndex(result.Lines, elapsed)

			if totalMs > 0 && elapsed >= totalMs+4000 {
				if nextItem, ok := pbSession.Dequeue(); ok {
					slog.Info("Advancing to next queued song on finish", "query", nextItem.Query, "key", playbackKey)
					go playQueuedSong(s, ic, pbSession, nextItem)
					return
				}

				pbSession.Stopped.Store(true)
				finishedSourceLabel := "LRCLIB"
				if result.Source == "spotify" {
					finishedSourceLabel = "Spotify"
				} else if result.Source == "petitlyrics" {
					finishedSourceLabel = "PetitLyrics"
				}
				result.mu.RLock()
				fModel := result.TranslationModel
				fLang := strings.ToUpper(strings.TrimSpace(result.DetectedLang))
				result.mu.RUnlock()
				var fFooter string
				if fModel != "" {
					if fLang != "" {
						fFooter = fmt.Sprintf("소스: %s • 번역: %s (%s) | %s / %s", finishedSourceLabel, fModel, fLang, fmtMs(result.DurationMs), fmtMs(result.DurationMs))
					} else {
						fFooter = fmt.Sprintf("소스: %s • 번역: %s | %s / %s", finishedSourceLabel, fModel, fmtMs(result.DurationMs), fmtMs(result.DurationMs))
					}
				} else {
					fFooter = fmt.Sprintf("소스: %s | %s / %s", finishedSourceLabel, fmtMs(totalMs), fmtMs(totalMs))
				}

				_, _ = editLyricsMessage(s, ic, pbSession, result.TrackName, result.Artist, "🎶 **가사 재생 완료**", fFooter, nil)
				return
			}

			if currentIdx == pbSession.LastIdx || busy {
				continue
			}

			busy = true
			pbSession.mu.Lock()
			pbSession.LastIdx = currentIdx
			pbSession.mu.Unlock()

			buttons := buildLyricsButtons(playbackKey, true, false, false, pbSession.IsAuto, pbSession.IsDedicated, pbSession.QueueLength())
			desc := buildLyricsDescription(result, currentIdx)
			footer := buildLyricsFooter(result, elapsed)

			_, err := editLyricsMessage(s, ic, pbSession, result.TrackName, result.Artist, desc, footer, buttons)
			if err != nil {
				slog.Warn("Failed to edit live lyrics", "error", err)
				pbSession.mu.Lock()
				pbSession.LastIdx = -1
				pbSession.mu.Unlock()
			}
			busy = false
		}
	}
}

func HandleLyricsComponent(session *discordgo.Session, ic *discordgo.InteractionCreate) {
	customID := ic.MessageComponentData().CustomID

	var action string
	var playbackKey string

	if strings.HasPrefix(customID, "lyrics_stop:") {
		action = "stop"
		playbackKey = strings.TrimPrefix(customID, "lyrics_stop:")
	} else if strings.HasPrefix(customID, "lyrics_queue_add:") {
		action = "queue_add"
		playbackKey = strings.TrimPrefix(customID, "lyrics_queue_add:")
	} else if strings.HasPrefix(customID, "lyrics_skip:") {
		action = "skip"
		playbackKey = strings.TrimPrefix(customID, "lyrics_skip:")
	} else if strings.HasPrefix(customID, "lyrics_fuzzy:") {
		action = "fuzzy"
		playbackKey = strings.TrimPrefix(customID, "lyrics_fuzzy:")
	} else if strings.HasPrefix(customID, "lyrics_refresh:") {
		action = "refresh"
		playbackKey = strings.TrimPrefix(customID, "lyrics_refresh:")
	} else if strings.HasPrefix(customID, "lyrics_offset_minus:") {
		action = "minus"
		playbackKey = strings.TrimPrefix(customID, "lyrics_offset_minus:")
	} else if strings.HasPrefix(customID, "lyrics_offset_plus:") {
		action = "plus"
		playbackKey = strings.TrimPrefix(customID, "lyrics_offset_plus:")
	} else {
		return
	}

	var pbSession *LyricsPlaybackSession
	if val, ok := activeLyricsPlaybacks.Load(playbackKey); ok && val != nil {
		pbSession = val.(*LyricsPlaybackSession)
	}

	if pbSession == nil {
		if action == "stop" {
			_ = session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseDeferredMessageUpdate,
			})
			_ = deleteLyricsMessage(session, ic, nil)
			return
		}
		_ = session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "이미 종료되었거나 존재하지 않는 가사 세션입니다.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	if pbSession.OwnerID != "" && !isInteractionOwnerOrAdmin(ic, pbSession.OwnerID) {
		_ = session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "가사를 재생한 사용자 또는 관리자만 조작할 수 있습니다.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	if action == "queue_add" {
		err := session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseModal,
			Data: &discordgo.InteractionResponseData{
				CustomID: "lyrics_queue_modal:" + playbackKey,
				Title:    "🎵 다음 곡 큐 추가",
				Components: []discordgo.MessageComponent{
					discordgo.ActionsRow{
						Components: []discordgo.MessageComponent{
							discordgo.TextInput{
								CustomID:    "queue_query",
								Label:       "곡 제목 / 아티스트 / 검색어",
								Style:       discordgo.TextInputShort,
								Placeholder: "예: 怪物 YOASOBI, 아이유 밤편지...",
								Required:    true,
								MaxLength:   100,
							},
						},
					},
				},
			},
		})
		if err != nil {
			slog.Error("Failed to open lyrics queue modal", "error", err)
		}
		return
	}

	if action == "stop" {
		_ = session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredMessageUpdate,
		})
		pbSession.Stopped.Store(true)
		select {
		case <-pbSession.Done:
		default:
			close(pbSession.Done)
		}
		pbSession.ClearQueue()
		activeLyricsPlaybacks.Delete(playbackKey)
		channelLyricsPlaybacks.Delete(pbSession.ChannelID)
		if redisClient != nil {
			_ = redisClient.DeleteChannelSpotifyState(pbSession.ChannelID)
		}
		_ = deleteLyricsMessage(session, ic, pbSession)
		return
	}

	_ = session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	})

	pbSession.mu.Lock()
	pbSession.Interaction = ic.Interaction
	if ic.Message != nil {
		pbSession.MessageID = ic.Message.ID
		if pbSession.ChannelID == "" {
			pbSession.ChannelID = ic.Message.ChannelID
		}
	}
	pbSession.mu.Unlock()

	if action == "skip" {
		if nextItem, ok := pbSession.Dequeue(); ok {
			go playQueuedSong(session, ic, pbSession, nextItem)
		} else {
			pbSession.TriggerImmediateUpdate(session, ic)
		}
		return
	}

	if action == "fuzzy" {
		pbSession.mu.RLock()
		title := pbSession.TrackTitle
		if title == "" {
			title = pbSession.SearchQuery
		}
		artist := pbSession.ArtistName
		coverURL := pbSession.CoverURL
		searchQ := pbSession.SearchQuery
		if searchQ == "" {
			searchQ = title
		}
		pbSession.mu.RUnlock()

		busyButtons := buildLyricsButtons(playbackKey, false, true, false, pbSession.IsAuto, pbSession.IsDedicated, pbSession.QueueLength())
		_, _ = editLyricsMessage(session, ic, pbSession, title, artist, "🔍 **유사 검색을 진행하는 중입니다...**", "가사 유사 검색 중", busyButtons)

		res, err := fetchLyricsConcurrent(searchQ, pbSession.TrackTitle, pbSession.ArtistName, true)
		if err != nil {
			slog.Warn("Fuzzy lyrics search failed", "query", searchQ, "track", pbSession.TrackTitle, "error", err)
			buttons := buildLyricsButtons(playbackKey, false, false, false, pbSession.IsAuto, pbSession.IsDedicated, pbSession.QueueLength())
			_, _ = editLyricsMessage(session, ic, pbSession, title, artist, "❌ **유사 검색으로도 일치하는 가사를 찾을 수 없습니다.**", "가사 검색 결과 없음", buttons)
			return
		}

		if coverURL != "" && res.CoverURL == "" {
			res.CoverURL = coverURL
		}

		pbSession.mu.Lock()
		pbSession.Result = res
		pbSession.StartTime = time.Now()
		pbSession.Offset = 0
		pbSession.LastIdx = -1
		pbSession.mu.Unlock()

		if pbSession.ImageMode && len(res.Lines) > 0 {
			pbSession.PreRenderLyricsFramesSync()
		}

		buttons := buildLyricsButtons(playbackKey, true, false, false, pbSession.IsAuto, pbSession.IsDedicated, pbSession.QueueLength())
		desc := buildLyricsDescription(res, 0)
		footer := buildLyricsFooter(res, 0)
		_, _ = editLyricsMessage(session, ic, pbSession, res.TrackName, res.Artist, desc, footer, buttons)

		if !pbSession.IsAuto {
			go runLyricsPlaybackLoop(session, ic, pbSession)
		}

		geminiKey := ""
		openAIKey := ""
		if botConfig != nil {
			geminiKey = botConfig.GeminiAPIKey
			openAIKey = botConfig.OpenAIAPIKey
		}
		if (geminiKey != "" || openAIKey != "") && len(res.Lines) > 0 {
			go func() {
				attachLyricsTranslation(res, geminiKey, openAIKey)
				if pbSession.ImageMode {
					pbSession.PreRenderLyricsFramesSync()
				}
				pbSession.mu.Lock()
				pbSession.LastIdx = -1
				pbSession.mu.Unlock()
				pbSession.TriggerImmediateUpdate(session, ic)
			}()
		}
		return
	}

	if action == "minus" {
		pbSession.AdjustOffset(-1 * time.Second)
		pbSession.TriggerImmediateUpdate(session, ic)
	} else if action == "plus" {
		pbSession.AdjustOffset(1 * time.Second)
		pbSession.TriggerImmediateUpdate(session, ic)
	} else if action == "refresh" {
		if pbSession.IsAuto {
			act := getSpotifyActivity(session, pbSession.OwnerID, ic.GuildID)
			if act == nil {
				pbSession.mu.Lock()
				pbSession.Result = nil
				pbSession.LastIdx = -1
				pbSession.mu.Unlock()

				buttons := buildLyricsButtons(playbackKey, false, false, false, pbSession.IsAuto, pbSession.IsDedicated, pbSession.QueueLength())
				title := "🎧 Spotify 실시간 가사 추적 대기 중"
				desc := "Spotify에서 노래를 재생하면 자동으로 가사와 실시간 번역이 시작됩니다...\n\n⏹️ **종료하려면 아래 중지 버튼을 눌러주세요.**"
				footer := "자동 추적 모드 활성화됨 (15분 연장 완료)"
				if pbSession.IsDedicated {
					footer = "상시 고정 전용 채널 • 자동 추적 대기 중"
				}
				_, _ = editLyricsMessage(session, ic, pbSession, title, "", desc, footer, buttons)
				return
			}

			trackTitle := strings.TrimSpace(act.Details)
			artistName := strings.TrimSpace(act.State)
			trackIdentity := fmt.Sprintf("%s|%s", strings.ToLower(trackTitle), strings.ToLower(artistName))

			pbSession.mu.RLock()
			currRes := pbSession.Result
			pbSession.mu.RUnlock()

			var isSameSong bool
			if currRes != nil {
				currIdentity := fmt.Sprintf("%s|%s", strings.ToLower(currRes.TrackName), strings.ToLower(currRes.Artist))
				if currIdentity == trackIdentity {
					isSameSong = true
				}
			}

			if isSameSong {
				if act.Timestamps.StartTimestamp > 0 {
					CalibrateWithSpotifyActivity(pbSession, act, currRes.TrackName, currRes.Artist)
					pbSession.mu.Lock()
					pbSession.LastIdx = -1
					pbSession.mu.Unlock()
					pbSession.TriggerImmediateUpdate(session, ic)
				}
			} else {
				searchQuery := trackTitle
				if artistName != "" {
					searchQuery = trackTitle + " " + artistName
				}
				res, err := fetchLyricsConcurrent(searchQuery, trackTitle, artistName, false)
				if err != nil {
					slog.Warn("HandleLyricsComponent: failed to fetch lyrics on refresh", "track", trackTitle, "artist", artistName, "error", err)
					notFoundDesc := "❌ **가사를 찾을 수 없습니다.**\n\n🔍 **유사 검색을 시도하려면 아래 버튼을 눌러주세요.**\n⏹️ **종료하려면 중지 버튼을 눌러주세요.**"
					buttons := buildLyricsButtons(playbackKey, false, false, true, pbSession.IsAuto, pbSession.IsDedicated, pbSession.QueueLength())

					var spotCover string
					if act != nil && strings.HasPrefix(act.Assets.LargeImageID, "spotify:") {
						spotCover = fmt.Sprintf("https://i.scdn.co/image/%s", strings.TrimPrefix(act.Assets.LargeImageID, "spotify:"))
					}

					pbSession.mu.Lock()
					pbSession.SearchQuery = searchQuery
					pbSession.TrackTitle = trackTitle
					pbSession.ArtistName = artistName
					pbSession.CoverURL = spotCover
					pbSession.Result = nil
					pbSession.LastIdx = -1
					pbSession.mu.Unlock()

					if pbSession.ImageMode {
						if pbSession.FramesDir == "" {
							pbSession.FramesDir = fmt.Sprintf("/tmp/lyrics_frames_%s", pbSession.Key)
						}
						_ = os.MkdirAll(pbSession.FramesDir, 0755)
						nfPath := fmt.Sprintf("%s/frame_000.jpg", pbSession.FramesDir)
						_ = RenderNotFoundLyricsFrame(spotCover, nfPath)
						pbSession.mu.Lock()
						pbSession.LastIdx = 0
						pbSession.mu.Unlock()
					}

					footer := "자동 추적 대기 중 (15분 연장 완료)"
					if pbSession.IsDedicated {
						footer = "상시 고정 전용 채널 • 자동 추적 대기 중"
					}
					_, _ = editLyricsMessage(session, ic, pbSession, trackTitle, artistName, notFoundDesc, footer, buttons)
					return
				}

				if strings.HasPrefix(act.Assets.LargeImageID, "spotify:") {
					res.CoverURL = fmt.Sprintf("https://i.scdn.co/image/%s", strings.TrimPrefix(act.Assets.LargeImageID, "spotify:"))
				}
				if act.Timestamps.EndTimestamp > act.Timestamps.StartTimestamp && act.Timestamps.StartTimestamp > 0 {
					res.DurationMs = int(act.Timestamps.EndTimestamp - act.Timestamps.StartTimestamp)
				}
				var sTime time.Time
				if act.Timestamps.StartTimestamp > 0 {
					sTime = time.UnixMilli(act.Timestamps.StartTimestamp)
				} else {
					sTime = time.Now()
				}

				pbSession.mu.Lock()
				pbSession.Result = res
				pbSession.StartTime = sTime
				pbSession.Offset = 0
				pbSession.LastIdx = -1
				pbSession.mu.Unlock()

				if pbSession.ImageMode && len(res.Lines) > 0 {
					pbSession.PreRenderLyricsFramesSync()
				}

				geminiKey := ""
				openAIKey := ""
				if botConfig != nil {
					geminiKey = botConfig.GeminiAPIKey
					openAIKey = botConfig.OpenAIAPIKey
				}

				targetRes := res
				if (geminiKey != "" || openAIKey != "") && len(targetRes.Lines) > 0 {
					go func() {
						attachLyricsTranslation(targetRes, geminiKey, openAIKey)
						if pbSession.ImageMode {
							pbSession.PreRenderLyricsFramesSync()
						}
						pbSession.mu.Lock()
						if pbSession.Result == targetRes {
							pbSession.LastIdx = -1
						}
						pbSession.mu.Unlock()
						pbSession.TriggerImmediateUpdate(session, ic)
					}()
				}

				pbSession.TriggerImmediateUpdate(session, ic)
			}
		} else {
			if pbSession.Result != nil && pbSession.OwnerID != "" {
				spAct := getSpotifyActivity(session, pbSession.OwnerID, ic.GuildID)
				if spAct != nil && spAct.Timestamps.StartTimestamp > 0 {
					CalibrateWithSpotifyActivity(pbSession, spAct, pbSession.Result.TrackName, pbSession.Result.Artist)
				}
			}
			pbSession.mu.Lock()
			pbSession.LastIdx = -1
			pbSession.mu.Unlock()
			pbSession.TriggerImmediateUpdate(session, ic)
		}
	}
}

func HandleLyricsQueueModalSubmit(session *discordgo.Session, ic *discordgo.InteractionCreate) {
	customID := ic.ModalSubmitData().CustomID
	playbackKey := strings.TrimPrefix(customID, "lyrics_queue_modal:")

	var pbSession *LyricsPlaybackSession
	if val, ok := activeLyricsPlaybacks.Load(playbackKey); ok && val != nil {
		pbSession = val.(*LyricsPlaybackSession)
	}

	if pbSession == nil {
		_ = session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "이미 종료되었거나 존재하지 않는 가사 세션입니다.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	if pbSession.OwnerID != "" && !isInteractionOwnerOrAdmin(ic, pbSession.OwnerID) {
		_ = session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "가사를 재생한 사용자 또는 관리자만 조작할 수 있습니다.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	var query string
	for _, comp := range ic.ModalSubmitData().Components {
		if ar, ok := comp.(*discordgo.ActionsRow); ok {
			for _, c := range ar.Components {
				if ti, ok := c.(*discordgo.TextInput); ok && ti.CustomID == "queue_query" {
					query = strings.TrimSpace(ti.Value)
				}
			}
		}
	}

	if query == "" {
		_ = session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "곡 제목이나 검색어를 입력해주세요.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	qItem, qLen := pbSession.Enqueue(query)
	go preloadQueuedLyricsItem(pbSession, qItem)

	_ = session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("✅ **%s** 곡을 대기열에 추가했습니다! (가사 및 번역 사전 준비 중 • 현재 대기: %d곡)", query, qLen),
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})

	pbSession.mu.RLock()
	currResult := pbSession.Result
	pbSession.mu.RUnlock()

	if currResult == nil || len(currResult.Lines) == 0 {
		if nextItem, ok := pbSession.Dequeue(); ok {
			go playQueuedSong(session, ic, pbSession, nextItem)
		}
	} else {
		pbSession.TriggerImmediateUpdate(session, ic)
	}
}
