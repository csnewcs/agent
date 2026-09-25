package main

import (
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

func buildDDayCommand() (BotCommand, error) {
	return NewBotCommandBuilder("dday").
		WithDescription("날짜 계산기 (오늘까지 지난 일수 계산)").
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionString,
			Name:        "date",
			Description: "기준 날짜 (기본값: 2025.05.10, 예: 2025.05.10, 2025-05-10)",
			Required:    false,
		}).
		WithFunction(handleDDayCommand).
		Build()
}

func buildDateCommand() (BotCommand, error) {
	return NewBotCommandBuilder("date").
		WithDescription("날짜 계산기 (오늘까지 지난 일수 계산)").
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionString,
			Name:        "date",
			Description: "기준 날짜 (기본값: 2025.05.10, 예: 2025.05.10, 2025-05-10)",
			Required:    false,
		}).
		WithFunction(handleDDayCommand).
		Build()
}

func handleDDayCommand(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	kstLocation := time.FixedZone("KST", 9*3600)
	now := time.Now().In(kstLocation)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, kstLocation)

	dateStr := "2025.05.10"
	options := ic.ApplicationCommandData().Options
	for _, opt := range options {
		if opt.Name == "date" && strings.TrimSpace(opt.StringValue()) != "" {
			dateStr = strings.TrimSpace(opt.StringValue())
		}
	}

	targetDate, err := parseDateString(dateStr, kstLocation)
	if err != nil {
		comps := SimpleErrorCard("날짜 형식이 올바르지 않습니다. (예: 2025.05.10 또는 2025-05-10)")
		_ = RespondComponentsV2(s, ic, comps, true)
		return
	}

	diff := today.Sub(targetDate)
	days := int(math.Round(diff.Hours() / 24))

	comps := NewComponentsBuilder().
		WithBody(fmt.Sprintf("%d일", days)).
		Build()

	err = RespondComponentsV2(s, ic, comps, false)
	if err != nil {
		slog.Error("Failed to respond to dday interaction", "error", err)
	}
}

func parseDateString(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, " ", "")
	formats := []string{
		"2006.01.02",
		"2006.1.2",
		"2006-01-02",
		"2006-1-2",
		"2006/01/02",
		"2006/1/2",
		"20060102",
	}
	for _, f := range formats {
		if t, err := time.ParseInLocation(f, s, loc); err == nil {
			return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid date format: %s", s)
}
