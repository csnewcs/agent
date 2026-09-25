package main

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/bwmarrin/discordgo"
)

func buildDeleteSessionCommand() (BotCommand, error) {
	return NewBotCommandBuilder("delete_session").
		WithDescription(".").
		WithIntegrationTypes(&[]discordgo.ApplicationIntegrationType{discordgo.ApplicationIntegrationUserInstall}).
		WithContexts(&[]discordgo.InteractionContextType{discordgo.InteractionContextGuild, discordgo.InteractionContextBotDM, discordgo.InteractionContextPrivateChannel}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:         discordgo.ApplicationCommandOptionString,
			Name:         "session_id_1",
			Description:  ".",
			Required:     true,
			Autocomplete: true,
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:         discordgo.ApplicationCommandOptionString,
			Name:         "session_id_2",
			Description:  ".",
			Required:     false,
			Autocomplete: true,
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:         discordgo.ApplicationCommandOptionString,
			Name:         "session_id_3",
			Description:  ".",
			Required:     false,
			Autocomplete: true,
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:         discordgo.ApplicationCommandOptionString,
			Name:         "session_id_4",
			Description:  ".",
			Required:     false,
			Autocomplete: true,
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:         discordgo.ApplicationCommandOptionString,
			Name:         "session_id_5",
			Description:  ".",
			Required:     false,
			Autocomplete: true,
		}).
		WithFunction(handleDeleteSessionCommand).
		Build()
}

func handleDeleteSessionCommand(s *discordgo.Session, ic *discordgo.InteractionCreate) {
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
		comps := SimpleErrorCard("세션 삭제에 실패했습니다.")
		_ = RespondComponentsV2(s, ic, comps, true)
		return
	}

	body := fmt.Sprintf("• **삭제된 세션**: `%s`", strings.Join(deletedList, ", "))

	if activeDeleted {
		newActive, err := GetActiveSessionID(db)
		if err != nil {
			newActive = 1
		}
		newActiveStr := strconv.Itoa(newActive)
		_ = ActivateSession(db, newActiveStr)
		atomic.StoreInt32(&currentSessionID, int32(newActive))
		slog.Info("Active session deleted, switched session", "new_active", newActive)

		body += fmt.Sprintf("\n• **새 활성 세션**: 세션 `%d`", newActive)
	}

	comps := NewComponentsBuilder().
		WithTitle("세션 삭제 완료").
		WithBody(body).
		WithFooter("대화 내역 및 세션 정보가 정리되었습니다.").
		Build()

	err := RespondComponentsV2(s, ic, comps, true)
	if err != nil {
		slog.Error("Failed to respond to delete_session interaction", "error", err)
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
