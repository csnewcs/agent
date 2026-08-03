package main

import (
	"encoding/json"
	"fmt"
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

	var payload map[string]ForecastItem
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload, nil
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

func handleForecastCommand(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	var location string
	options := ic.ApplicationCommandData().Options
	for _, opt := range options {
		if opt.Name == "location" {
			location = opt.StringValue()
		}
	}

	_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
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

		// Sort time keys chronologically (e.g. "20260803-1200", "20260803-1500")
		var keys []string
		for k := range forecastMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		// Group forecast items by Date ("YYYY년 MM월 DD일")
		type DateGroup struct {
			DateLabel string
			Lines     []string
		}
		var dateGroups []*DateGroup
		groupMap := make(map[string]*DateGroup)

		for _, k := range keys {
			parts := strings.Split(k, "-")
			if len(parts) != 2 || len(parts[0]) != 8 || len(parts[1]) != 4 {
				continue
			}

			dateStr := parts[0]
			timeStr := parts[1]

			t, err := time.Parse("20060102", dateStr)
			var dateLabel string
			if err == nil {
				dateLabel = t.Format("2006년 01월 02일")
			} else {
				dateLabel = dateStr
			}

			hourMinStr := fmt.Sprintf("%s:%s", timeStr[:2], timeStr[2:])
			item := forecastMap[k]

			wEmoji := getWeatherEmoji(item.Weather)
			if item.Weather == "" {
				wEmoji = getWeatherEmoji(item.Cloud)
			}

			tempStr := item.Temperature
			if tempStr != "" {
				tempStr = tempStr + "°C"
			} else {
				tempStr = "-"
			}

			appTempStr := formatApparentTemp(item)
			if appTempStr != "" {
				tempStr = fmt.Sprintf("%s (체감 %s)", tempStr, appTempStr)
			}

			popStr := item.ProbabilityOfPrecipitation
			if popStr != "" {
				popStr = popStr + "%"
			} else {
				popStr = "0%"
			}

			precipStr := item.Precipitation
			if precipStr == "" {
				precipStr = "0mm"
			}

			line := fmt.Sprintf("• `%s` %s %s | 🌡️ **%s** | 💧 %s%% | 🌧️ %s (%s)",
				hourMinStr, wEmoji, item.Weather, tempStr, item.Humidity, precipStr, popStr)

			grp, exists := groupMap[dateLabel]
			if !exists {
				grp = &DateGroup{
					DateLabel: dateLabel,
					Lines:     []string{},
				}
				groupMap[dateLabel] = grp
				dateGroups = append(dateGroups, grp)
			}
			grp.Lines = append(grp.Lines, line)
		}

		embed := &discordgo.MessageEmbed{
			Title:       "🌤️ 실시간 단기예보 정보",
			Description: fmt.Sprintf("📍 위치: **%s** (격자: %d, %d)", pos.Address, pos.X, pos.Y),
			Color:       0x3498db,
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
			Footer: &discordgo.MessageEmbedFooter{
				Text: "기상청 단기예보 기준",
			},
		}

		for _, grp := range dateGroups {
			if len(embed.Fields) >= 25 {
				break
			}

			valStr := strings.Join(grp.Lines, "\n")
			runes := []rune(valStr)
			if len(runes) > 1000 {
				valStr = string(runes[:990]) + "\n... (외 이하 생략)"
			}

			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   fmt.Sprintf("📅 %s (%d개 예보)", grp.DateLabel, len(grp.Lines)),
				Value:  valStr,
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
