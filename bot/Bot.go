package main

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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

	if err := InitTJDB(config); err != nil {
		slog.Error("Failed to initialize TJ DB connection", "error", err)
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
		} else if interaction.Type == discordgo.InteractionModalSubmit {
			RunModalSubmit(session, interaction)
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
			Description: "응답 비공개 여부 (미선택 시 100자 이하 공개, 100자 초과 비공개)",
			Required:    false,
		}).
		WithFunction(func(s *discordgo.Session, ic *discordgo.InteractionCreate) {
			query := getInteractionOptionString(ic, "query")
			if query == "" {
				return
			}

			ephemeralVal, hasEphemeralOpt := getInteractionOptionBool(ic, "ephemeral")

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
				if err := handleAIInteraction(config, s, ic, query, ephemeralVal, hasEphemeralOpt); err != nil {
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

	goHomeCmd, err := NewBotCommandBuilder("gohome").
		WithDescription("오늘 퇴근까지 남은 시간을 알려줍니다.").
		WithIntegrationTypes(&[]discordgo.ApplicationIntegrationType{
			discordgo.ApplicationIntegrationUserInstall,
			discordgo.ApplicationIntegrationGuildInstall,
		}).
		WithContexts(&[]discordgo.InteractionContextType{
			discordgo.InteractionContextGuild,
			discordgo.InteractionContextBotDM,
			discordgo.InteractionContextPrivateChannel,
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionString,
			Name:        "person",
			Description: "인원 선택 (기본값: 배재현)",
			Required:    false,
			Choices: []*discordgo.ApplicationCommandOptionChoice{
				{
					Name:  "배재현",
					Value: "배재현",
				},
				{
					Name:  "임태현",
					Value: "임태현",
				},
				{
					Name:  "박민혁",
					Value: "박민혁",
				},
			},
		}).
		WithFunction(func(s *discordgo.Session, ic *discordgo.InteractionCreate) {
			person := getInteractionOptionString(ic, "person")
			if person == "" {
				person = "배재현"
			}

			kstLocation := time.FixedZone("KST", 9*3600)
			now := time.Now().In(kstLocation)

			isHoliday := isKoreanHoliday(now)

			var targetHour int
			var isOff bool

			switch person {
			case "임태현":
				targetHour = 17
				isOff = (now.Weekday() == time.Saturday || now.Weekday() == time.Sunday || isHoliday)
			case "박민혁":
				if now.Weekday() == time.Sunday {
					isOff = true
				} else if now.Weekday() == time.Saturday || isHoliday {
					targetHour = 18
				} else {
					targetHour = 22
				}
			default: // 배재현
				targetHour = 18
				isOff = (now.Weekday() == time.Saturday || now.Weekday() == time.Sunday || isHoliday)
			}

			if isOff || (targetHour > 0 && now.Hour() >= targetHour) {
				_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: "오늘은 주말, 공휴일이거나 이미 퇴근 시간이 지났습니다.",
						Flags:   discordgo.MessageFlagsEphemeral,
					},
				})
				return
			}

			target := time.Date(now.Year(), now.Month(), now.Day(), targetHour, 0, 0, 0, kstLocation)
			diff := target.Sub(now)

			h := int(diff.Hours())
			m := int(diff.Minutes()) % 60
			sec := int(diff.Seconds()) % 60

			var timeStr string
			if h > 0 {
				timeStr = fmt.Sprintf("%d시간 %d분 %d초", h, m, sec)
			} else {
				timeStr = fmt.Sprintf("%d분 %d초", m, sec)
			}

			content := fmt.Sprintf("퇴근: %s", timeStr)

			err := s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: content,
				},
			})
			if err != nil {
				slog.Error("Failed to respond to gohome interaction", "error", err)
			}
		}).Build()
	if err != nil {
		slog.Error("Error occured when build command", "error", err)
		return
	}

	cCmd, err := NewBotCommandBuilder("c").
		WithDescription("호스트 시스템에서 리눅스 커맨드를 실행합니다.").
		WithIntegrationTypes(&[]discordgo.ApplicationIntegrationType{
			discordgo.ApplicationIntegrationUserInstall,
			discordgo.ApplicationIntegrationGuildInstall,
		}).
		WithContexts(&[]discordgo.InteractionContextType{
			discordgo.InteractionContextGuild,
			discordgo.InteractionContextBotDM,
			discordgo.InteractionContextPrivateChannel,
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionString,
			Name:        "command",
			Description: "실행할 리눅스 커맨드",
			Required:    true,
		}).
		WithFunction(handleCCommand).
		Build()
	weatherCmd, err := NewBotCommandBuilder("weather").
		WithDescription("실시간 날씨 정보를 조회합니다.").
		WithIntegrationTypes(&[]discordgo.ApplicationIntegrationType{
			discordgo.ApplicationIntegrationUserInstall,
			discordgo.ApplicationIntegrationGuildInstall,
		}).
		WithContexts(&[]discordgo.InteractionContextType{
			discordgo.InteractionContextGuild,
			discordgo.InteractionContextBotDM,
			discordgo.InteractionContextPrivateChannel,
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionString,
			Name:        "location",
			Description: "조회할 위치/지역명 (기본값: 성남시 수정구 태평1동)",
			Required:    false,
		}).
		WithFunction(handleWeatherCommand).
		Build()
	if err != nil {
		slog.Error("Error occured when build command", "error", err)
		return
	}

	tjCmd, err := NewBotCommandBuilder("tj").
		WithDescription("TJ 노래방 트래킹 목록(아티스트/곡)을 관리합니다.").
		WithIntegrationTypes(&[]discordgo.ApplicationIntegrationType{
			discordgo.ApplicationIntegrationUserInstall,
			discordgo.ApplicationIntegrationGuildInstall,
		}).
		WithContexts(&[]discordgo.InteractionContextType{
			discordgo.InteractionContextGuild,
			discordgo.InteractionContextBotDM,
			discordgo.InteractionContextPrivateChannel,
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionSubCommand,
			Name:        "add",
			Description: "트래킹 대상(아티스트 또는 곡)을 추가합니다.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "category",
					Description: "구분 (artist 또는 song)",
					Required:    true,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "artist (아티스트)", Value: "artist"},
						{Name: "song (곡 제목)", Value: "song"},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "name",
					Description: "추가할 아티스트명 또는 곡 제목",
					Required:    true,
				},
			},
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionSubCommand,
			Name:        "delete",
			Description: "트래킹 대상(아티스트 또는 곡)을 삭제합니다.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "category",
					Description: "구분 (artist 또는 song)",
					Required:    true,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "artist (아티스트)", Value: "artist"},
						{Name: "song (곡 제목)", Value: "song"},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "name",
					Description: "삭제할 아티스트명 또는 곡 제목",
					Required:    true,
				},
			},
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionSubCommand,
			Name:        "list",
			Description: "현재 등록된 트래킹 목록을 조회합니다.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "category",
					Description: "조회할 카테고리 (all, artist 또는 song)",
					Required:    false,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "all (전체)", Value: "all"},
						{Name: "artist (아티스트)", Value: "artist"},
						{Name: "song (곡 제목)", Value: "song"},
					},
				},
			},
		}).
		WithFunction(handleTJCommand).
		Build()
	if err != nil {
		slog.Error("Error occured when build command tj", "error", err)
		return
	}

	forecastCmd, err := NewBotCommandBuilder("forecast").
		WithDescription("실시간 단기예보 정보를 조회합니다.").
		WithIntegrationTypes(&[]discordgo.ApplicationIntegrationType{
			discordgo.ApplicationIntegrationUserInstall,
			discordgo.ApplicationIntegrationGuildInstall,
		}).
		WithContexts(&[]discordgo.InteractionContextType{
			discordgo.InteractionContextGuild,
			discordgo.InteractionContextBotDM,
			discordgo.InteractionContextPrivateChannel,
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionString,
			Name:        "location",
			Description: "조회할 위치/지역명 (기본값: 성남시 수정구 태평1동)",
			Required:    false,
		}).
		WithFunction(handleForecastCommand).
		Build()
	if err != nil {
		slog.Error("Error occured when build command forecast", "error", err)
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

	commands := []BotCommand{pingCmd, askCmd, deleteSessionCmd, statsCmd, kepcoCmd, goHomeCmd, cCmd, weatherCmd, tjCmd, forecastCmd}
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

func getInteractionOptionBool(ic *discordgo.InteractionCreate, name string) (bool, bool) {
	for _, option := range ic.ApplicationCommandData().Options {
		if option.Name == name && option.Value != nil {
			if b, ok := option.Value.(bool); ok {
				return b, true
			}
			if s, ok := option.Value.(string); ok {
				return s == "true", true
			}
		}
	}
	return false, false
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
	if strings.HasPrefix(customID, "c_input:") || strings.HasPrefix(customID, "c_stop:") {
		HandleCmdComponent(session, ic)
		return
	}

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

		var existingContent string
		if ic.Message != nil {
			existingContent = ic.Message.Content
		}
		if existingContent == "" {
			existingContent = responseText
		}

		if responseText == "" {
			_ = session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseUpdateMessage,
				Data: &discordgo.InteractionResponseData{
					Content:    "답변을 찾을 수 없습니다.",
					Components: []discordgo.MessageComponent{},
				},
			})
			return
		}

		// 1. Remove button from the ephemeral message while preserving its text content
		err := session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Content:    existingContent,
				Components: []discordgo.MessageComponent{},
			},
		})
		if err != nil {
			slog.Error("Failed to update component interaction", "error", err)
		}

		// 2. Publish original answer publicly via interaction followup (works in user-install & channel contexts)
		if err := sendSplitFollowupMessages(session, ic, responseText); err != nil {
			slog.Error("Failed to publish response via followup, trying channel send fallback", "error", err)
			_ = sendSplitChannelMessages(session, ic.ChannelID, responseText)
		}
	}
}

func RunModalSubmit(session *discordgo.Session, ic *discordgo.InteractionCreate) {
	customID := ic.ModalSubmitData().CustomID
	if strings.HasPrefix(customID, "c_modal:") {
		HandleCmdModalSubmit(session, ic)
	}
}

func isKoreanHoliday(t time.Time) bool {
	// Fixed solar holidays (MM-DD)
	fixedHolidays := map[string]bool{
		"01-01": true, // 신정
		"03-01": true, // 삼일절
		"05-05": true, // 어린이날
		"06-06": true, // 현충일
		"08-15": true, // 광복절
		"10-03": true, // 개천절
		"10-09": true, // 한글날
		"12-25": true, // 성탄절
	}

	mmdd := t.Format("01-02")
	if fixedHolidays[mmdd] {
		return true
	}

	// Lunar & substitute holidays (YYYY-MM-DD)
	lunarHolidays := map[string]bool{
		// 2025
		"2025-01-28": true, "2025-01-29": true, "2025-01-30": true,
		"2025-03-03": true, "2025-05-06": true,
		"2025-10-05": true, "2025-10-06": true, "2025-10-07": true, "2025-10-08": true,

		// 2026
		"2026-02-16": true, "2026-02-17": true, "2026-02-18": true,
		"2026-03-02": true, "2026-05-24": true, "2026-05-25": true,
		"2026-08-17": true, "2026-09-24": true, "2026-09-25": true, "2026-09-26": true,
		"2026-10-05": true,

		// 2027
		"2027-02-06": true, "2027-02-07": true, "2027-02-08": true, "2027-02-09": true,
		"2027-05-13": true, "2027-08-16": true,
		"2027-09-14": true, "2027-09-15": true, "2027-09-16": true,
		"2027-10-04": true, "2027-10-11": true,
	}

	yyyymmdd := t.Format("2006-01-02")
	return lunarHolidays[yyyymmdd]
}
