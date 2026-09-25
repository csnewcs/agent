package main

import (
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/bwmarrin/discordgo"
)

var (
	db               *DBClient
	botID            string
	botConfig        *Config
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
	botConfig = config
	var err error
	db, err = NewDBClient(config)
	if err != nil {
		return nil, err
	}

	if err := InitTJDB(config); err != nil {
		slog.Error("Failed to initialize TJ DB connection", "error", err)
	}

	if err := InitHomeDB(config); err != nil {
		slog.Error("Failed to initialize Home DB connection", "error", err)
	}

	if err := InitRedis(config.RedisURL); err != nil {
		slog.Error("Failed to initialize Redis connection", "error", err)
	}

	if err := InitAntigravityProxy(config.AntigravityProxyPath); err != nil {
		slog.Error("Failed to initialize Antigravity Proxy process", "error", err)
	}

	InitAntigravityBotStartup()
	InitCodexProxy()
	InitCodexBotStartup()

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

	session.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentGuildPresences

	session.AddHandler(func(session *discordgo.Session, interaction *discordgo.InteractionCreate) {
		switch interaction.Type {
		case discordgo.InteractionApplicationCommand:
			RunCommand(session, interaction)
		case discordgo.InteractionApplicationCommandAutocomplete:
			RunAutocomplete(session, interaction)
		case discordgo.InteractionMessageComponent:
			RunComponent(session, interaction)
		case discordgo.InteractionModalSubmit:
			RunModalSubmit(session, interaction)
		}
	})

	session.AddHandler(func(session *discordgo.Session, p *discordgo.PresenceUpdate) {
		cacheSpotifyPresenceUpdate(p)
	})

	session.AddHandler(func(session *discordgo.Session, ready *discordgo.Ready) {
		latestSpotifyActivities.Clear()
		botID = ready.User.ID
		slog.Info("Bot is ready")
		go makeCommands(session, config)
		go StartAntigravityRecoveryWorker(session)
		go StartCodexRecoveryWorker(session)
		go StartSpotifyRecoveryWorker(session)
	})

	err = session.Open()
	if err != nil {
		db.Close()
		return nil, err
	}

	return session, nil
}

func makeCommands(session *discordgo.Session, config *Config) {
	builders := []func() (BotCommand, error){
		buildAntigravityCommand,
		buildCodexCommand,
		func() (BotCommand, error) { return buildAskCommand(config) },
		buildDeleteSessionCommand,
		buildStatsCommand,
		buildKepcoCommand,
		buildGoHomeCommand,
		buildCCommand,
		buildWeatherCommand,
		buildTJCommand,
		buildHomeCommand,
		buildWebLoginCommand,
		buildForecastCommand,
		buildHyeongCommand,
		buildHyeongMessageEncodeCommand,
		buildHyeongMessageDecodeCommand,
		buildAskMessageCommand,
		buildQuoteMessageCommand,
		buildLyricsCommand,
		buildDDayCommand,
		buildDateCommand,
		buildQrCommand,
		buildQrCodeCommand,
		buildQuizCommand,
	}

	for _, build := range builders {
		cmd, err := build()
		if err != nil {
			slog.Error("Failed to build command", "error", err)
			continue
		}
		if config.Mode == "development" && config.DefaultServerID != "" {
			err = cmd.RegisterGuild(session, config.DefaultServerID)
		} else {
			err = cmd.RegisterGlobal(session)
		}
		if err != nil {
			slog.Error("Failed to register command", "name", cmd.name, "error", err)
			continue
		}
		slog.Info("Command registered", "Name", cmd.name)
	}

	// Clean up deprecated commands
	globalCmds, err := session.ApplicationCommands(session.State.User.ID, "")
	if err == nil {
		for _, cmd := range globalCmds {
			if cmd.Name == "spotify" || cmd.Name == "refresh_session" || cmd.Name == "change_session" || cmd.Name == "ping" {
				_ = session.ApplicationCommandDelete(session.State.User.ID, "", cmd.ID)
				slog.Info("Deleted obsolete command", "Name", cmd.Name)
			}
		}
	}
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
	if !isSlashCommandAllowed(getUserID(ic)) {
		return
	}

	data := ic.ApplicationCommandData()

	switch data.Name {
	case "tj":
		SendTJDeleteAutocomplete(session, ic)
	case "delete_session":
		sendSessionAutocomplete(session, ic, false)
	case "ask":
		for _, opt := range data.Options {
			if opt.Name == "session" && opt.Focused {
				sendSessionAutocomplete(session, ic, true)
				return
			}
		}
	case "antigravity":
		for _, opt := range data.Options {
			if opt.Type == discordgo.ApplicationCommandOptionSubCommand {
				for _, subOpt := range opt.Options {
					if (subOpt.Name == "project" || subOpt.Name == "project_id") && subOpt.Focused {
						sendAntigravityProjectAutocomplete(session, ic)
						return
					}
				}
			} else {
				if (opt.Name == "project" || opt.Name == "project_id") && opt.Focused {
					sendAntigravityProjectAutocomplete(session, ic)
					return
				}
			}
		}
	case "codex":
		for _, opt := range data.Options {
			if opt.Type == discordgo.ApplicationCommandOptionSubCommand {
				for _, subOpt := range opt.Options {
					if subOpt.Name == "project" && subOpt.Focused {
						sendCodexProjectAutocomplete(session, ic)
						return
					}
				}
			}
		}
	}
}

func RunComponent(session *discordgo.Session, ic *discordgo.InteractionCreate) {
	customID := ic.MessageComponentData().CustomID

	switch {
	case strings.HasPrefix(customID, "tj_"):
		HandleTJComponent(session, ic)
	case strings.HasPrefix(customID, "forecast_"):
		handleForecastDetailComponent(session, ic)
	case strings.HasPrefix(customID, "home_"):
		handleHomeComponent(session, ic)
	case strings.HasPrefix(customID, "c_input:"), strings.HasPrefix(customID, "c_stop:"):
		HandleCmdComponent(session, ic)
	case strings.HasPrefix(customID, "publish_ask:"):
		handlePublishAskComponent(session, ic)
	case strings.HasPrefix(customID, "aiq_"):
		HandleAskMessageComponent(session, ic)
	case strings.HasPrefix(customID, "ag_"):
		HandleAntigravityComponent(session, ic)
	case strings.HasPrefix(customID, "codex_"):
		HandleCodexComponent(session, ic)
	case strings.HasPrefix(customID, "lyrics_"):
		HandleLyricsComponent(session, ic)
	case strings.HasPrefix(customID, "quiz_"):
		handleQuizComponent(session, ic)
	}
}

func RunModalSubmit(session *discordgo.Session, ic *discordgo.InteractionCreate) {
	customID := ic.ModalSubmitData().CustomID
	if strings.HasPrefix(customID, "c_modal:") {
		HandleCmdModalSubmit(session, ic)
	} else if strings.HasPrefix(customID, "ag_modal:") {
		HandleAntigravityModalSubmit(session, ic)
	} else if strings.HasPrefix(customID, "lyrics_queue_modal:") {
		HandleLyricsQueueModalSubmit(session, ic)
	} else if strings.HasPrefix(customID, "aiq_modal:") {
		HandleAskMessageModalSubmit(session, ic)
	}
}
