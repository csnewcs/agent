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

func getWeatherTheme(wStr string) (string, int) {
	switch {
	case strings.Contains(wStr, "맑음"):
		return "☀️", 0xF1C40F // Amber Gold
	case strings.Contains(wStr, "구름") || strings.Contains(wStr, "흐림"):
		return "☁️", 0x95A5A6 // Muted Gray
	case strings.Contains(wStr, "비"):
		return "🌧️", 0x3498DB // Ocean Blue
	case strings.Contains(wStr, "눈"):
		return "❄️", 0x00D2D3 // Cyan White
	case strings.Contains(wStr, "소나기"):
		return "🌦️", 0x2980B9 // Deep Blue
	default:
		return "🌤️", 0x3498DB
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

func makeHumidityBar(humStr string) string {
	var h float64
	_, err := fmt.Sscan(humStr, &h)
	if err != nil {
		return humStr + "%"
	}
	filled := int(h / 10.0)
	if filled > 10 {
		filled = 10
	}
	if filled < 0 {
		filled = 0
	}
	empty := 10 - filled
	return fmt.Sprintf("[%s%s] %.0f%%", strings.Repeat("█", filled), strings.Repeat("░", empty), h)
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
		emoji, color := getWeatherTheme(w.Weather)
		windText := getWindDirectionName(w.WindDirectionDegree)
		humidityBar := makeHumidityBar(w.Humidity)

		descHeader := fmt.Sprintf("### %s **%s°C** · %s\n> 🌡️ 체감 **%.1f°C**  │  💧 습도 **%s%%**",
			emoji, w.Temperature, w.Weather, w.ApparentTemperature, w.Humidity)

		dashboardBlock := fmt.Sprintf("```text\n"+
			"┌──────────────────────────────────────────────┐\n"+
			"│ 🌡️ 기  온 │ %-32s │\n"+
			"│ 💧 습  도 │ %-32s │\n"+
			"│ 🌧️ 강수량 │ %-32s │\n"+
			"│ 💨 풍  속 │ %-32s │\n"+
			"│ 🧭 풍  향 │ %-32s │\n"+
			"└──────────────────────────────────────────────┘\n"+
			"```",
			fmt.Sprintf("%s °C (체감 %.1f °C)", w.Temperature, w.ApparentTemperature),
			humidityBar,
			fmt.Sprintf("%s mm", w.Precipitation),
			fmt.Sprintf("%s m/s", w.WindSpeed),
			windText,
		)

		embed := &discordgo.MessageEmbed{
			Title:       fmt.Sprintf("%s 실시간 기상 상태 브리핑", emoji),
			Description: descHeader + "\n\n" + dashboardBlock,
			Color:       color,
			Footer: &discordgo.MessageEmbedFooter{
				Text: "실시간 기상 모니터링",
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
