package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

type WeatherPayload struct {
	Weather struct {
		Weather              string  `json:"weather"`
		Temperature          string  `json:"temperature"`
		ApparentTemperature float64 `json:"apparent_temperature"`
		Humidity             string  `json:"humidity"`
		Precipitation        string  `json:"precipitation"`
		WindSpeed            string  `json:"wind_speed"`
		WindDirectionDegree  string  `json:"wind_direction_degree"`
		WindU                string  `json:"wind_u"`
		WindV                string  `json:"wind_v"`
	} `json:"weather"`
}

func fetchWeatherInfo() (*WeatherPayload, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://127.0.0.1:5678/webhook/weather")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP status %d", resp.StatusCode)
	}

	var payload WeatherPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func getWeatherEmoji(wStr string) string {
	switch {
	case strings.Contains(wStr, "맑음"):
		return "☀️"
	case strings.Contains(wStr, "구름") || strings.Contains(wStr, "흐림"):
		return "☁️"
	case strings.Contains(wStr, "비"):
		return "🌧️"
	case strings.Contains(wStr, "눈"):
		return "❄️"
	case strings.Contains(wStr, "소나기"):
		return "🌦️"
	default:
		return "🌤️"
	}
}

func handleWeatherCommand(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	go func() {
		wPayload, err := fetchWeatherInfo()
		if err != nil {
			slog.Error("Failed to fetch weather info", "error", err)
			errMsg := fmt.Sprintf("날씨 정보를 가져오는데 실패했습니다: %v", err)
			_, _ = s.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{
				Content: &errMsg,
			})
			return
		}

		w := wPayload.Weather
		emoji := getWeatherEmoji(w.Weather)

		embed := &discordgo.MessageEmbed{
			Title: fmt.Sprintf("%s 실시간 날씨 정보", emoji),
			Color: 0x3498db,
			Fields: []*discordgo.MessageEmbedField{
				{
					Name:   "날씨 상태",
					Value:  fmt.Sprintf("%s %s", emoji, w.Weather),
					Inline: true,
				},
				{
					Name:   "기온 / 체감 온도",
					Value:  fmt.Sprintf("%s°C (체감 %.1f°C)", w.Temperature, w.ApparentTemperature),
					Inline: true,
				},
				{
					Name:   "습도",
					Value:  fmt.Sprintf("%s%%", w.Humidity),
					Inline: true,
				},
				{
					Name:   "강수량",
					Value:  fmt.Sprintf("%s mm", w.Precipitation),
					Inline: true,
				},
				{
					Name:   "풍속 / 풍향",
					Value:  fmt.Sprintf("%s m/s (%s°)", w.WindSpeed, w.WindDirectionDegree),
					Inline: true,
				},
			},
			Footer: &discordgo.MessageEmbedFooter{
				Text: "실시간 기상 정보",
			},
			Timestamp: time.Now().Format(time.RFC3339),
		}

		embeds := []*discordgo.MessageEmbed{embed}
		_, editErr := s.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{
			Embeds: &embeds,
		})
		if editErr != nil {
			slog.Error("Failed to edit interaction response for /weather", "error", editErr)
		}
	}()
}
