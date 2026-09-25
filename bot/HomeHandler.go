package main

import (
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/bwmarrin/discordgo"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type HomeDBClient struct {
	db *sql.DB
}

var homeDB *HomeDBClient

func InitHomeDB(config *Config) error {
	db, err := sql.Open("pgx", config.HomeDBURL)
	if err != nil {
		return fmt.Errorf("failed to open home db: %w", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		return fmt.Errorf("failed to ping home db: %w", err)
	}

	homeDB = &HomeDBClient{db: db}
	slog.Info("Successfully connected to Home database", "url", config.HomeDBURL)
	return nil
}

type HomeEnvData struct {
	CurrentTemp  float64
	TempTime     time.Time
	CurrentHumid float64
	HumidTime    time.Time
}

func (c *HomeDBClient) GetOverview() (*HomeEnvData, error) {
	if c == nil || c.db == nil {
		return nil, fmt.Errorf("home database is not connected")
	}

	query := `
	WITH latest_t AS (
		SELECT temp, createdat FROM temperature ORDER BY createdat DESC LIMIT 1
	),
	latest_h AS (
		SELECT humid, createdat FROM humidity ORDER BY createdat DESC LIMIT 1
	)
	SELECT
		latest_t.temp, latest_t.createdat,
		latest_h.humid, latest_h.createdat
	FROM latest_t, latest_h;
	`

	var d HomeEnvData
	err := c.db.QueryRow(query).Scan(
		&d.CurrentTemp, &d.TempTime,
		&d.CurrentHumid, &d.HumidTime,
	)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func calculateDiscomfortIndex(temp, humid float64) (float64, string) {
	di := (1.8 * temp) - (0.55 * (1.0 - (humid / 100.0)) * ((1.8 * temp) - 26.0)) + 32.0
	var level string
	switch {
	case di < 68:
		level = "낮음 (쾌적)"
	case di < 75:
		level = "보통"
	case di < 80:
		level = "높음"
	default:
		level = "매우 높음"
	}
	return di, level
}

func buildHomeCommand() (BotCommand, error) {
	return NewBotCommandBuilder("home").
		WithDescription("집 내부 온도, 습도 및 불쾌지수를 확인합니다.").
		WithFunction(handleHomeCommand).
		Build()
}

func handleHomeCommand(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	comps, err := getHomeComponents()
	if err != nil {
		errComps := SimpleErrorCard(fmt.Sprintf("집 공기 데이터를 조회하지 못했습니다: %v", err))
		_ = RespondComponentsV2(s, ic, errComps, true)
		return
	}

	_ = RespondComponentsV2(s, ic, comps, false)
}

func handleHomeComponent(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	comps, err := getHomeComponents()
	if err != nil {
		errComps := SimpleErrorCard(fmt.Sprintf("새로고침 중 오류가 발생했습니다: %v", err))
		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Components: errComps,
				Flags:      discordgo.MessageFlagsIsComponentsV2,
			},
		})
		return
	}

	_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Components: comps,
			Flags:      discordgo.MessageFlagsIsComponentsV2,
		},
	})
}

func getHomeComponents() ([]discordgo.MessageComponent, error) {
	if homeDB == nil {
		return nil, fmt.Errorf("홈 데이터베이스가 초기화되지 않았습니다")
	}

	data, err := homeDB.GetOverview()
	if err != nil {
		return nil, err
	}

	di, diLevel := calculateDiscomfortIndex(data.CurrentTemp, data.CurrentHumid)

	body := fmt.Sprintf("• **온도**: `%.2f°C`\n• **습도**: `%.2f%%`\n• **CO2**: `(추후 연동 예정)`\n• **불쾌지수**: `%s` (%.1f)",
		data.CurrentTemp, data.CurrentHumid, diLevel, di)

	btnRefresh := discordgo.Button{
		Label:    "새로고침",
		Style:    discordgo.PrimaryButton,
		CustomID: "home_refresh",
	}

	comps := NewComponentsBuilder().
		WithTitle("실시간 집 내부 환경 정보").
		WithBody(body).
		WithFooter(fmt.Sprintf("센서 측정 시각: %s", data.TempTime.Format("15:04:05"))).
		WithButtons(btnRefresh).
		Build()

	return comps, nil
}
