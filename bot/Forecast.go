package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

type ForecastItem struct {
	Weather                    string      `json:"weather"`
	Cloud                      string      `json:"cloud"`
	Temperature                string      `json:"temperature"`
	ApparentTemperature        interface{} `json:"apparent_temperature"`
	ApperentTemperature        interface{} `json:"apperent_temperature"` // Typo fallback
	Humidity                   string      `json:"humidity"`
	Precipitation              string      `json:"precipitation"`
	ProbabilityOfPrecipitation string      `json:"probability_of_precipitation"`
	WindSpeed                  string      `json:"wind_speed"`
	WindDirectionDegree        string      `json:"wind_direction_degree"`
	WindU                      string      `json:"wind_u"`
	WindV                      string      `json:"wind_v"`
}

func fetchForecastInfo(pos *LocationPos) (map[string]ForecastItem, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	reqURL := fmt.Sprintf("http://127.0.0.1:5678/webhook/forecast?address=%s&x=%d&y=%d",
		url.QueryEscape(pos.Address), pos.X, pos.Y)

	resp, err := client.Get(reqURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP status %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Try unmarshaling as { "forecasts": map[string]ForecastItem }
	var resWrapper struct {
		Forecasts map[string]ForecastItem `json:"forecasts"`
	}
	if err := json.Unmarshal(bodyBytes, &resWrapper); err == nil && len(resWrapper.Forecasts) > 0 {
		return resWrapper.Forecasts, nil
	}

	// Fallback to direct map[string]ForecastItem
	var directMap map[string]ForecastItem
	if err := json.Unmarshal(bodyBytes, &directMap); err == nil {
		return directMap, nil
	}

	return nil, fmt.Errorf("failed to parse forecast JSON response")
}

func formatApparentTemp(item ForecastItem) string {
	var val interface{}
	if item.ApparentTemperature != nil {
		val = item.ApparentTemperature
	} else if item.ApperentTemperature != nil {
		val = item.ApperentTemperature
	}

	if val == nil {
		return ""
	}

	switch v := val.(type) {
	case float64:
		if v == 0 {
			return ""
		}
		return fmt.Sprintf("%.1f°C", v)
	case string:
		if v == "" || v == "0" {
			return ""
		}
		return v + "°C"
	default:
		return fmt.Sprintf("%v°C", v)
	}
}

func buildDailySummary(dateStr string, items []string, forecastMap map[string]ForecastItem) (string, string) {
	t, err := time.Parse("20060102", dateStr)
	var dateLabel string
	if err == nil {
		weekdays := []string{"일", "월", "화", "수", "목", "금", "토"}
		dateLabel = fmt.Sprintf("%s (%s)", t.Format("2006년 01월 02일"), weekdays[t.Weekday()])
	} else {
		dateLabel = dateStr
	}

	var minTemp, maxTemp float64
	hasTemp := false
	var maxAppTemp float64
	hasAppTemp := false
	var minHum, maxHum int
	hasHum := false
	var maxPOP int
	hasPOP := false
	var maxWind float64
	hasWind := false

	weatherCounts := make(map[string]int)
	rainSnow := ""

	checkpointSet := map[string]bool{
		"0900": true, "1500": true, "2100": true,
	}
	var checkpoints []string

	for _, k := range items {
		item := forecastMap[k]
		parts := strings.Split(k, "-")
		if len(parts) == 2 && checkpointSet[parts[1]] {
			checkpoints = append(checkpoints, k)
		}

		// Temp
		if item.Temperature != "" {
			var tempVal float64
			if _, err := fmt.Sscanf(item.Temperature, "%f", &tempVal); err == nil {
				if !hasTemp {
					minTemp, maxTemp = tempVal, tempVal
					hasTemp = true
				} else {
					if tempVal < minTemp {
						minTemp = tempVal
					}
					if tempVal > maxTemp {
						maxTemp = tempVal
					}
				}
			}
		}

		// Apparent Temp
		appTempStr := formatApparentTemp(item)
		if appTempStr != "" {
			var appVal float64
			if _, err := fmt.Sscanf(appTempStr, "%f", &appVal); err == nil {
				if !hasAppTemp || appVal > maxAppTemp {
					maxAppTemp = appVal
					hasAppTemp = true
				}
			}
		}

		// Humidity
		if item.Humidity != "" {
			var humVal int
			if _, err := fmt.Sscanf(item.Humidity, "%d", &humVal); err == nil {
				if !hasHum {
					minHum, maxHum = humVal, humVal
					hasHum = true
				} else {
					if humVal < minHum {
						minHum = humVal
					}
					if humVal > maxHum {
						maxHum = humVal
					}
				}
			}
		}

		// POP
		if item.ProbabilityOfPrecipitation != "" {
			var popVal int
			if _, err := fmt.Sscanf(item.ProbabilityOfPrecipitation, "%d", &popVal); err == nil {
				if !hasPOP || popVal > maxPOP {
					maxPOP = popVal
					hasPOP = true
				}
			}
		}

		// Wind
		if item.WindSpeed != "" {
			var windVal float64
			if _, err := fmt.Sscanf(item.WindSpeed, "%f", &windVal); err == nil {
				if !hasWind || windVal > maxWind {
					maxWind = windVal
					hasWind = true
				}
			}
		}

		// Weather
		wName := item.Weather
		if wName == "" {
			wName = item.Cloud
		}
		if wName != "" {
			weatherCounts[wName]++
			if strings.Contains(wName, "비") || strings.Contains(wName, "눈") || strings.Contains(wName, "소나기") {
				rainSnow = wName
			}
		}
	}

	// Fallback checkpoints if standard 0900/1500/2100 are missing for current day
	if len(checkpoints) == 0 && len(items) > 0 {
		step := len(items) / 3
		if step < 1 {
			step = 1
		}
		for i := 0; i < len(items) && len(checkpoints) < 3; i += step {
			checkpoints = append(checkpoints, items[i])
		}
	}

	// Representative weather
	repWeather := "맑음"
	if rainSnow != "" {
		repWeather = rainSnow
	} else {
		maxCnt := 0
		for w, cnt := range weatherCounts {
			if cnt > maxCnt {
				maxCnt = cnt
				repWeather = w
			}
		}
	}
	repEmoji := getWeatherEmoji(repWeather)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s **대표 날씨**: %s\n", repEmoji, repWeather))

	if hasTemp {
		if minTemp == maxTemp {
			sb.WriteString(fmt.Sprintf("🌡️ **기온**: **%.0f°C**", maxTemp))
		} else {
			sb.WriteString(fmt.Sprintf("🌡️ **기온**: **%.0f°C ~ %.0f°C**", minTemp, maxTemp))
		}
		if hasAppTemp {
			sb.WriteString(fmt.Sprintf(" (최고 체감 **%.1f°C**)", maxAppTemp))
		}
		sb.WriteString("\n")
	}

	var metaParts []string
	if hasPOP {
		metaParts = append(metaParts, fmt.Sprintf("🌧️ **강수확률**: 최대 **%d%%**", maxPOP))
	}
	if hasHum {
		if minHum == maxHum {
			metaParts = append(metaParts, fmt.Sprintf("💧 **습도**: **%d%%**", maxHum))
		} else {
			metaParts = append(metaParts, fmt.Sprintf("💧 **습도**: **%d%% ~ %d%%**", minHum, maxHum))
		}
	}
	if hasWind {
		metaParts = append(metaParts, fmt.Sprintf("💨 **풍속**: 최대 **%.1fm/s**", maxWind))
	}

	if len(metaParts) > 0 {
		sb.WriteString(strings.Join(metaParts, " | ") + "\n")
	}

	return dateLabel, sb.String()
}

func handleForecastCommand(s *discordgo.Session, ic *discordgo.InteractionCreate) {
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
		forecastMap, err := fetchForecastInfo(pos)
		if err != nil {
			slog.Error("Failed to fetch forecast info", "location", pos.Address, "error", err)
			errMsg := fmt.Sprintf("단기예보 정보를 가져오는데 실패했습니다: %v", err)
			_, _ = s.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{
				Content: &errMsg,
			})
			return
		}

		if len(forecastMap) == 0 {
			errMsg := "단기예보 데이터가 없습니다."
			_, _ = s.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{
				Content: &errMsg,
			})
			return
		}

		// Sort keys
		var keys []string
		for k := range forecastMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		// Group keys by YYYYMMDD date string
		dateMap := make(map[string][]string)
		var dateOrder []string

		for _, k := range keys {
			parts := strings.Split(k, "-")
			if len(parts) != 2 || len(parts[0]) != 8 {
				continue
			}
			dStr := parts[0]
			if _, exists := dateMap[dStr]; !exists {
				dateOrder = append(dateOrder, dStr)
			}
			dateMap[dStr] = append(dateMap[dStr], k)
		}

		embed := &discordgo.MessageEmbed{
			Title:       "🌤️ 실시간 단기예보 요약",
			Description: fmt.Sprintf("📍 위치: **%s** (격자: %d, %d)", pos.Address, pos.X, pos.Y),
			Color:       0x3498db,
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
			Footer: &discordgo.MessageEmbedFooter{
				Text: "기상청 단기예보 기준",
			},
		}

		for _, dStr := range dateOrder {
			if len(embed.Fields) >= 25 {
				break
			}
			items := dateMap[dStr]
			dateLabel, summaryVal := buildDailySummary(dStr, items, forecastMap)

			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   fmt.Sprintf("📅 %s", dateLabel),
				Value:  summaryVal,
				Inline: false,
			})
		}

		embeds := []*discordgo.MessageEmbed{embed}
		_, editErr := s.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{
			Embeds: &embeds,
		})
		if editErr != nil {
			slog.Error("Failed to edit interaction response for /forecast", "error", editErr)
		}
	}()
}
