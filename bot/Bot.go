package main

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/bwmarrin/discordgo"
)

var (
	db               *DBClient
	botID            string
	currentSessionID int32 = 1
)

func getSessionID() string {
	return strconv.Itoa(int(atomic.LoadInt32(&currentSessionID)))
}

func incrementSessionID() string {
	newVal := atomic.AddInt32(&currentSessionID, 1)
	return strconv.Itoa(int(newVal))
}

func InitBot(config *Config) (*discordgo.Session, error) {
	var err error
	db, err = NewDBClient(config)
	if err != nil {
		return nil, err
	}

	activeSession, err := GetActiveSessionID(db)
	if err != nil {
		slog.Error("Failed to load active session ID from DB", "error", err)
	} else {
		atomic.StoreInt32(&currentSessionID, int32(activeSession))
		slog.Info("Loaded active session ID from DB", "session_id", activeSession)
		_ = ActivateSession(db, strconv.Itoa(activeSession))
	}

	session, err := discordgo.New("Bot " + config.Token)
	if err != nil {
		db.Close()
		return nil, err
	}

	session.Identify.Intents = discordgo.IntentsGuilds

	session.AddHandler(func(session *discordgo.Session, interaction *discordgo.InteractionCreate) {
		if interaction.Type == discordgo.InteractionApplicationCommand {
			RunCommand(session, interaction)
		} else if interaction.Type == discordgo.InteractionApplicationCommandAutocomplete {
			RunAutocomplete(session, interaction)
		} else if interaction.Type == discordgo.InteractionMessageComponent {
			RunComponent(session, interaction)
		}
	})

	session.AddHandler(func(session *discordgo.Session, ready *discordgo.Ready) {
		botID = ready.User.ID
		makeCommands(session, config)
		slog.Info("Bot is ready")
	})

	// session.AddHandler(func(session *discordgo.Session, message *discordgo.MessageCreate) {
	// 	if message.Author == nil || message.Author.Bot {
	// 		return
	// 	}
	// 	if config.DefaultChannelID == "" || message.ChannelID != config.DefaultChannelID {
	// 		return
	// 	}
	// 	if messageMentionsBot(message, botID) {
	// 		query := stripBotMention(message.Content, botID)
	// 		if query == "" {
	// 			return
	// 		}
	// 		go func(msg *discordgo.MessageCreate) {
	// 			if err := handleAIRequest(config, session, msg, query); err != nil {
	// 				slog.Error("Failed to send mention request to n8n", "error", err)
	// 
	// 			}
	// 		}(message)
	// 	} else if err := SaveMessage(db, message); err != nil {
	// 		slog.Error("Failed to save message", "error", err)
	// 		return
	// 	}
	// })

	err = session.Open()
	if err != nil {
		db.Close()
		return nil, err
	}

	return session, nil
}

func makeCommands(session *discordgo.Session, config *Config) {
	pingCmd, err := NewBotCommandBuilder("ping").WithDescription("봇의 핑을 테스트합니다.").WithFunction(func(s *discordgo.Session, ic *discordgo.InteractionCreate) {
		err := s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "퐁!",
			},
		})
		if err != nil {
			slog.Error("Failed to respond to interaction", "error", err)
		}
	}).Build()
	if err != nil {
		slog.Error("Error occured when build command", "error", err)
		return
	}

	askCmd, err := NewBotCommandBuilder("ask").
		WithDescription("AI에게 질문합니다.").
		WithIntegrationTypes(&[]discordgo.ApplicationIntegrationType{discordgo.ApplicationIntegrationUserInstall}).
		WithContexts(&[]discordgo.InteractionContextType{discordgo.InteractionContextGuild, discordgo.InteractionContextBotDM, discordgo.InteractionContextPrivateChannel}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionString,
			Name:        "query",
			Description: "질문 내용",
			Required:    true,
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:         discordgo.ApplicationCommandOptionString,
			Name:         "session",
			Description:  "세션 선택: 새 세션(new) 또는 전환할 세션 ID",
			Required:     false,
			Autocomplete: true,
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionBoolean,
			Name:        "ephemeral",
			Description: "응답 비공개 여부 (기본값: true, false 지정 시 전체 공개)",
			Required:    false,
		}).
		WithFunction(func(s *discordgo.Session, ic *discordgo.InteractionCreate) {
			query := getInteractionOptionString(ic, "query")
			if query == "" {
				return
			}

			ephemeral := getInteractionOptionBoolWithDefault(ic, "ephemeral", true)

			sessionArg := getInteractionOptionString(ic, "session")
			if sessionArg == "new" {
				// Create new session
				newSession := incrementSessionID()
				if err := ActivateSession(db, newSession); err != nil {
					slog.Error("Failed to activate new session in DB", "error", err)
				}
				slog.Info("Created new session via ask command", "session_id", newSession)
			} else if sessionArg != "" {
				// Switch to existing session
				var val int
				if _, err := fmt.Sscan(sessionArg, &val); err == nil {
					if err := ActivateSession(db, sessionArg); err != nil {
						slog.Error("Failed to activate session via ask", "error", err)
					} else {
						atomic.StoreInt32(&currentSessionID, int32(val))
						slog.Info("Switched session via ask command", "session_id", val)
					}
				}
			}

			go func() {
				if err := handleAIInteraction(config, s, ic, query, ephemeral); err != nil {
					slog.Error("Failed to handle ask command", "error", err)
				}
			}()
		}).Build()
	if err != nil {
		slog.Error("Error occured when build command", "error", err)
		return
	}

	deleteSessionCmd, err := NewBotCommandBuilder("delete_session").
		WithDescription("지정한 세션(들)과 대화 내역을 데이터베이스에서 삭제합니다.").
		WithIntegrationTypes(&[]discordgo.ApplicationIntegrationType{discordgo.ApplicationIntegrationUserInstall}).
		WithContexts(&[]discordgo.InteractionContextType{discordgo.InteractionContextGuild, discordgo.InteractionContextBotDM, discordgo.InteractionContextPrivateChannel}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:         discordgo.ApplicationCommandOptionString,
			Name:         "session_id_1",
			Description:  "삭제할 첫 번째 세션 ID",
			Required:     true,
			Autocomplete: true,
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:         discordgo.ApplicationCommandOptionString,
			Name:         "session_id_2",
			Description:  "삭제할 두 번째 세션 ID (선택)",
			Required:     false,
			Autocomplete: true,
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:         discordgo.ApplicationCommandOptionString,
			Name:         "session_id_3",
			Description:  "삭제할 세 번째 세션 ID (선택)",
			Required:     false,
			Autocomplete: true,
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:         discordgo.ApplicationCommandOptionString,
			Name:         "session_id_4",
			Description:  "삭제할 네 번째 세션 ID (선택)",
			Required:     false,
			Autocomplete: true,
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:         discordgo.ApplicationCommandOptionString,
			Name:         "session_id_5",
			Description:  "삭제할 다섯 번째 세션 ID (선택)",
			Required:     false,
			Autocomplete: true,
		}).
		WithFunction(func(s *discordgo.Session, ic *discordgo.InteractionCreate) {
			var targetSessions []string
			for _, optName := range []string{"session_id_1", "session_id_2", "session_id_3", "session_id_4", "session_id_5"} {
				val := getInteractionOptionString(ic, optName)
				if val != "" {
					targetSessions = append(targetSessions, val)
				}
			}

			if len(targetSessions) == 0 {
				val := getInteractionOptionString(ic, "session_id")
				if val != "" {
					targetSessions = append(targetSessions, val)
				}
			}

			if len(targetSessions) == 0 {
				return
			}

			var deletedList []string
			var activeDeleted bool
			for _, targetSession := range targetSessions {
				wasActive, err := DeleteSession(db, targetSession)
				if err != nil {
					slog.Error("Failed to delete session", "session_id", targetSession, "error", err)
					continue
				}
				deletedList = append(deletedList, targetSession)
				if wasActive {
					activeDeleted = true
				}
			}

			if len(deletedList) == 0 {
				_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: "세션 삭제에 실패했습니다.",
						Flags:   discordgo.MessageFlagsEphemeral,
					},
				})
				return
			}

			responseContent := fmt.Sprintf("세션 %s 및 관련 대화 내역이 성공적으로 삭제되었습니다.", strings.Join(deletedList, ", "))

			if activeDeleted {
				newActive, err := GetActiveSessionID(db)
				if err != nil {
					newActive = 1
				}
				newActiveStr := strconv.Itoa(newActive)
				_ = ActivateSession(db, newActiveStr)
				atomic.StoreInt32(&currentSessionID, int32(newActive))
				slog.Info("Active session deleted, switched session", "new_active", newActive)

				responseContent += fmt.Sprintf("\n현재 활성화된 세션이 세션 %d(으)로 변경되었습니다.", newActive)
			}

			err = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: responseContent,
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			if err != nil {
				slog.Error("Failed to respond to interaction", "error", err)
			}
		}).Build()
	if err != nil {
		slog.Error("Error occured when build command", "error", err)
		return
	}

	kepcoCmd, err := NewBotCommandBuilder("kepco").
		WithDescription("KEPCO 실시간 및 예상 전력 요금 조회를 수행합니다.").
		WithIntegrationTypes(&[]discordgo.ApplicationIntegrationType{discordgo.ApplicationIntegrationUserInstall}).
		WithContexts(&[]discordgo.InteractionContextType{discordgo.InteractionContextGuild, discordgo.InteractionContextBotDM, discordgo.InteractionContextPrivateChannel}).
		WithFunction(func(s *discordgo.Session, ic *discordgo.InteractionCreate) {
			initialEmbeds := []*discordgo.MessageEmbed{
				{
					Title: "실시간 전력 사용량 및 요금 조회 결과",
					Color: 0x2ecc71,
					Fields: []*discordgo.MessageEmbedField{
						{
							Name:   "KEPCO 스마트 사용량 통계",
							Value:  "```\n조회 중...\n```",
							Inline: false,
						},
					},
				},
			}
			err := s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Embeds: initialEmbeds,
				},
			})
			if err != nil {
				slog.Error("Failed to respond to KEPCO interaction", "error", err)
				return
			}

			go func() {
				res := collectKepcoUsage()
				embeds := []*discordgo.MessageEmbed{
					{
						Title: "실시간 전력 사용량 및 요금 조회 결과",
						Color: 0x2ecc71,
						Fields: []*discordgo.MessageEmbedField{
							{
								Name:   "KEPCO 스마트 사용량 통계",
								Value:  res,
								Inline: false,
							},
						},
					},
				}
				_, err = s.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{
					Embeds: &embeds,
				})
				if err != nil {
					slog.Error("Failed to edit KEPCO response", "error", err)
				}
			}()
		}).Build()
	if err != nil {
		slog.Error("Error occured when build command", "error", err)
		return
	}

	statsCmd, err := NewBotCommandBuilder("stats").
		WithDescription("현재 서버 상태 및 OpenAI 토큰 사용량을 측정하여 보여줍니다.").
		WithIntegrationTypes(&[]discordgo.ApplicationIntegrationType{discordgo.ApplicationIntegrationUserInstall}).
		WithContexts(&[]discordgo.InteractionContextType{discordgo.InteractionContextGuild, discordgo.InteractionContextBotDM, discordgo.InteractionContextPrivateChannel}).
		WithFunction(func(s *discordgo.Session, ic *discordgo.InteractionCreate) {
			var mu sync.Mutex
			serverStatus := "```\n측정 중...\n```"
			pingStatus := "```\n측정 중...\n```"
			tokenStatus := "```\n측정 중...\n```"

			updateMessage := func() {
				mu.Lock()
				embeds := []*discordgo.MessageEmbed{
					{
						Title: "시스템 및 서비스 상태 측정 결과",
						Color: 0x3498db,
						Fields: []*discordgo.MessageEmbedField{
							{
								Name:   "서버 자원 상태",
								Value:  serverStatus,
								Inline: false,
							},
							{
								Name:   "1.1.1.1 핑 상태",
								Value:  pingStatus,
								Inline: false,
							},
							{
								Name:   "OpenAI 토큰 사용량",
								Value:  tokenStatus,
								Inline: false,
							},
						},
					},
				}
				mu.Unlock()

				_, err := s.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{
					Embeds: &embeds,
				})
				if err != nil {
					slog.Error("Failed to edit interaction response in stats", "error", err)
				}
			}

			// Respond initially
			initialEmbeds := []*discordgo.MessageEmbed{
				{
					Title: "시스템 및 서비스 상태 측정 결과",
					Color: 0x3498db,
					Fields: []*discordgo.MessageEmbedField{
						{
							Name:   "서버 자원 상태",
							Value:  "```\n측정 중...\n```",
							Inline: false,
						},
						{
							Name:   "1.1.1.1 핑 상태",
							Value:  "```\n측정 중...\n```",
							Inline: false,
						},
						{
							Name:   "OpenAI 토큰 사용량",
							Value:  "```\n측정 중...\n```",
							Inline: false,
						},
					},
				},
			}
			err := s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Embeds: initialEmbeds,
				},
			})
			if err != nil {
				slog.Error("Failed to respond to stats interaction", "error", err)
				return
			}

			// Run tasks in parallel
			go func() {
				res := collectServerStats()
				mu.Lock()
				serverStatus = res
				mu.Unlock()
				updateMessage()
			}()

			go func() {
				res := collectPingStats()
				mu.Lock()
				pingStatus = res
				mu.Unlock()
				updateMessage()
			}()

			go func() {
				res := collectOpenAITokens()
				mu.Lock()
				tokenStatus = res
				mu.Unlock()
				updateMessage()
			}()
		}).Build()
	if err != nil {
		slog.Error("Error occured when build command", "error", err)
		return
	}

	// Clean up deprecated commands
	globalCmds, err := session.ApplicationCommands(session.State.User.ID, "")
	if err == nil {
		for _, cmd := range globalCmds {
			if cmd.Name == "refresh_session" || cmd.Name == "change_session" {
				_ = session.ApplicationCommandDelete(session.State.User.ID, "", cmd.ID)
				slog.Info("Deleted obsolete command", "Name", cmd.Name)
			}
		}
	}

	commands := []BotCommand{pingCmd, askCmd, deleteSessionCmd, statsCmd, kepcoCmd}
	for _, command := range commands {
		err = command.RegisterGlobal(session)
		if err != nil {
			slog.Error("Error occured when register command", "error", err)
			continue
		}
		slog.Info("Command is registered", "Name", command.name)
	}
}

func messageMentionsBot(message *discordgo.MessageCreate, botID string) bool {
	if botID == "" {
		return false
	}
	for _, mention := range message.Mentions {
		if mention.ID == botID {
			return true
		}
	}
	return strings.Contains(message.Content, "<@"+botID+">") || strings.Contains(message.Content, "<@!"+botID+">")
}

func stripBotMention(content, botID string) string {
	content = strings.ReplaceAll(content, "<@"+botID+">", "")
	content = strings.ReplaceAll(content, "<@!"+botID+">", "")
	return strings.TrimSpace(content)
}

func getInteractionOptionString(ic *discordgo.InteractionCreate, name string) string {
	for _, option := range ic.ApplicationCommandData().Options {
		if option.Name == name && option.Value != nil {
			return fmt.Sprint(option.Value)
		}
	}
	return ""
}

func getInteractionOptionBoolWithDefault(ic *discordgo.InteractionCreate, name string, defaultValue bool) bool {
	for _, option := range ic.ApplicationCommandData().Options {
		if option.Name == name && option.Value != nil {
			if b, ok := option.Value.(bool); ok {
				return b
			}
			if s, ok := option.Value.(string); ok {
				return s == "true"
			}
		}
	}
	return defaultValue
}

func KillBot(session *discordgo.Session, config *Config) {
	for _, cmd := range registeredCommands {
		err := DeleteCommand(session, cmd.name, "")
		if err != nil {
			slog.Error("Error occured when delete command", "error", err)
		}
	}
	if db != nil {
		_ = db.Close()
	}
	session.Close()
}

func RunAutocomplete(session *discordgo.Session, ic *discordgo.InteractionCreate) {
	data := ic.ApplicationCommandData()

	switch data.Name {
	case "delete_session":
		sendSessionAutocomplete(session, ic, false)
	case "ask":
		// Only autocomplete the 'session' option
		for _, opt := range data.Options {
			if opt.Name == "session" && opt.Focused {
				sendSessionAutocomplete(session, ic, true)
				return
			}
		}
	}
}

func sendSessionAutocomplete(session *discordgo.Session, ic *discordgo.InteractionCreate, includeNew bool) {
	sessions, err := GetSessions(db)
	if err != nil {
		slog.Error("Failed to fetch sessions for autocomplete", "error", err)
		return
	}

	choices := []*discordgo.ApplicationCommandOptionChoice{}

	if includeNew {
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
			Name:  "+ 새 세션 시작",
			Value: "new",
		})
	}

	for _, s := range sessions {
		name := fmt.Sprintf("세션 %s: %s", s.SessionID, s.Title)
		if s.Title == "" {
			name = fmt.Sprintf("세션 %s (제목 없음)", s.SessionID)
		}
		if s.IsActive {
			name = "[현재] " + name
		}
		if len([]rune(name)) > 100 {
			name = string([]rune(name)[:97]) + "..."
		}
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
			Name:  name,
			Value: s.SessionID,
		})
	}

	err = session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionApplicationCommandAutocompleteResult,
		Data: &discordgo.InteractionResponseData{
			Choices: choices,
		},
	})
	if err != nil {
		slog.Error("Failed to respond to autocomplete", "error", err)
	}
}

func RunComponent(session *discordgo.Session, ic *discordgo.InteractionCreate) {
	customID := ic.MessageComponentData().CustomID
	if strings.HasPrefix(customID, "publish_ask:") {
		pubID := strings.TrimPrefix(customID, "publish_ask:")
		var responseText string

		if val, ok := pendingPublishMap.LoadAndDelete(pubID); ok {
			if str, ok := val.(string); ok {
				responseText = str
			}
		}

		if responseText == "" && ic.Message != nil {
			responseText = ic.Message.Content
		}

		if responseText == "" {
			_ = session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseUpdateMessage,
				Data: &discordgo.InteractionResponseData{
					Components: []discordgo.MessageComponent{},
				},
			})
			return
		}

		// 1. Remove button from the ephemeral message
		err := session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Components: []discordgo.MessageComponent{},
			},
		})
		if err != nil {
			slog.Error("Failed to update component interaction", "error", err)
		}

		// 2. Send original answer to the channel for everyone to see
		if err := sendSplitChannelMessages(session, ic.ChannelID, responseText); err != nil {
			slog.Error("Failed to publish response to channel", "error", err)
		}
	}
}
