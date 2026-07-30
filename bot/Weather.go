package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
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

type LocationPos struct {
	Address string
	X       int
	Y       int
}

func getLocationCoordinates(dbClient *DBClient, queryStr string) *LocationPos {
	defaultPos := &LocationPos{
		Address: "경기도 성남시수정구 태평1동",
		X:       62,
		Y:       124,
	}

	if dbClient == nil {
		return defaultPos
	}

	queryStr = strings.TrimSpace(queryStr)
	if queryStr == "" {
		queryStr = "성남시 수정구 태평1동"
	}

	var pos LocationPos
	err := dbClient.QueryRow(`
		SELECT address, x, y
		FROM weather_pos
		ORDER BY similarity(address, $1) DESC
		LIMIT 1
	`, queryStr).Scan(&pos.Address, &pos.X, &pos.Y)

	if err != nil {
		slog.Error("Failed to lookup location coordinates", "query", queryStr, "error", err)
		return defaultPos
	}

	return &pos
}

func fetchWeatherInfo(pos *LocationPos) (*WeatherPayload, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	reqURL := fmt.Sprintf("http://127.0.0.1:5678/webhook/weather?address=%s&x=%d&y=%d",
		url.QueryEscape(pos.Address), pos.X, pos.Y)

	resp, err := client.Get(reqURL)
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

func getKMAObservationTime(now time.Time) time.Time {
	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		loc = time.FixedZone("KST", 9*3600)
	}
	nowKST := now.In(loc)

	year, month, day := nowKST.Date()
	hour := nowKST.Hour()

	// 기상청 초단기실황은 매시 30분 관측 데이터가 매시 40분에 API로 배포됨
	if nowKST.Minute() < 40 {
		hour--
	}

	return time.Date(year, month, day, hour, 30, 0, 0, loc)
}

func handleWeatherCommand(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	var location string
	options := ic.ApplicationCommandData().Options
	for _, opt := range options {
		if opt.Name == "location" {
			location = opt.StringValue()
		}
	}

	_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	go func() {
		pos := getLocationCoordinates(db, location)
		wPayload, err := fetchWeatherInfo(pos)
		if err != nil {
			slog.Error("Failed to fetch weather info", "location", pos.Address, "error", err)
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
			fmt.Sprintf("📍 위치: **%s** (격자: %d, %d)", pos.Address, pos.X, pos.Y),
			fmt.Sprintf("%s 날씨: %s", wEmoji, weatherName),
			fmt.Sprintf("🌡️ 온도: %s°C (체감온도: %.1f°C)", w.Temperature, w.ApparentTemperature),
			fmt.Sprintf("💧 습도: %s%%", w.Humidity),
			fmt.Sprintf("🌧️ 강수량: %smm", w.Precipitation),
			fmt.Sprintf("💨 풍속: %sm/s (풍향: %s)", w.WindSpeed, windText),
		}

		obsTime := getKMAObservationTime(time.Now())

		embed := &discordgo.MessageEmbed{
			Title:       "실시간 날씨 정보",
			Description: strings.Join(lines, "\n"),
			Color:       0x3498db,
			Footer: &discordgo.MessageEmbedFooter{
				Text: "관측 기준 시각",
			},
			Timestamp: obsTime.Format(time.RFC3339),
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
