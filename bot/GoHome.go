package main

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/bwmarrin/discordgo"
)

func buildGoHomeCommand() (BotCommand, error) {
	return NewBotCommandBuilder("gohome").
		WithDescription("오늘 퇴근까지 남은 시간을 계산합니다.").
		WithFunction(handleGoHomeCommand).
		Build()
}

func handleGoHomeCommand(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	kstLocation := time.FixedZone("KST", 9*3600)
	now := time.Now().In(kstLocation)

	target, ok := getOffWorkTarget(now, kstLocation)
	if !ok || !now.Before(target) {
		comps := SimpleErrorCard("오늘은 주말, 공휴일이거나 이미 퇴근 시간이 지났습니다. 편안한 휴식 되세요!")
		_ = RespondComponentsV2(s, ic, comps, true)
		return
	}

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

	comps := NewComponentsBuilder().
		WithBody(fmt.Sprintf("• **남은 시간**: **%s**", timeStr)).
		Build()

	err := RespondComponentsV2(s, ic, comps, false)
	if err != nil {
		slog.Error("Failed to respond to gohome interaction", "error", err)
	}
}

func getOffWorkTarget(t time.Time, loc *time.Location) (time.Time, bool) {
	if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday || isKoreanHoliday(t) {
		return time.Time{}, false
	}

	var hour, min int
	switch t.Weekday() {
	case time.Monday, time.Wednesday, time.Friday:
		hour, min = 17, 30
	case time.Tuesday, time.Thursday:
		hour, min = 17, 0
	default:
		return time.Time{}, false
	}

	return time.Date(t.Year(), t.Month(), t.Day(), hour, min, 0, 0, loc), true
}

func isKoreanHoliday(t time.Time) bool {
	fixedHolidays := map[string]bool{
		"01-01": true, "03-01": true, "05-05": true, "06-06": true,
		"08-15": true, "10-03": true, "10-09": true, "12-25": true,
	}

	mmdd := t.Format("01-02")
	if fixedHolidays[mmdd] {
		return true
	}

	lunarHolidays := map[string]bool{
		"2025-01-28": true, "2025-01-29": true, "2025-01-30": true,
		"2025-03-03": true, "2025-05-06": true, "2025-10-05": true,
		"2025-10-06": true, "2025-10-07": true, "2025-10-08": true,
		"2026-02-16": true, "2026-02-17": true, "2026-02-18": true,
		"2026-03-02": true, "2026-05-24": true, "2026-05-25": true,
		"2026-08-17": true, "2026-09-24": true, "2026-09-25": true,
		"2026-09-26": true, "2026-10-05": true,
		"2027-02-06": true, "2027-02-07": true, "2027-02-08": true,
		"2027-02-09": true, "2027-05-13": true, "2027-08-16": true,
		"2027-09-14": true, "2027-09-15": true, "2027-09-16": true,
		"2027-10-04": true, "2027-10-11": true,
	}

	yyyymmdd := t.Format("2006-01-02")
	return lunarHolidays[yyyymmdd]
}
