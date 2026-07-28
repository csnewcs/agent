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
	client := &http.Client{Timeout: 15 * time.Second}
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
	switch wStr {
	case "맑음":
		return "☀️"
	case "비":
		return "🌧️"
	case "비/눈":
		return "🌨️"
	case "눈":
		return "❄️"
	case "빗방울":
		return "🌦️"
	case "빗방울눈날림":
		return "🌨️"
	case "눈날림":
		return "❄️💨"
	case "":
		return "🌤️"
	default:
		switch {
		case strings.Contains(wStr, "맑음"):
			return "☀️"
		case strings.Contains(wStr, "비"):
			return "🌧️"
		case strings.Contains(wStr, "눈"):
			return "❄️"
		default:
			return "🌤️"
		}
	}
}

func getWindDirectionName(degStr string) string {
	var deg float64
	_, err := fmt.Sscan(degStr, &deg)
	if err != nil {
		return degStr + "°"
	}
	directions := []string{"북풍", "북동풍", "동풍", "남동풍", "남풍", "남서풍", "서풍", "북서풍"}
	idx := int((deg + 22.5) / 45.0) % 8
	return fmt.Sprintf("%.0f° (%s)", deg, directions[idx])
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
		weatherName := w.Weather
		if weatherName == "" {
			weatherName = "정보 없음"
		}
		wEmoji := getWeatherEmoji(w.Weather)
		windText := getWindDirectionName(w.WindDirectionDegree)

		lines := []string{
			fmt.Sprintf("%s 날씨: %s", wEmoji, weatherName),
			fmt.Sprintf("🌡️ 온도: %s°C (체감온도: %.1f°C)", w.Temperature, w.ApparentTemperature),
			fmt.Sprintf("💧 습도: %s%%", w.Humidity),
			fmt.Sprintf("🌧️ 강수량: %smm", w.Precipitation),
			fmt.Sprintf("💨 풍속: %sm/s (풍향: %s)", w.WindSpeed, windText),
		}

		embed := &discordgo.MessageEmbed{
			Title:       "실시간 날씨 정보",
			Description: strings.Join(lines, "\n"),
			Color:       0x3498db,
			Timestamp:   time.Now().Format(time.RFC3339),
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
