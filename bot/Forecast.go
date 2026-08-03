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
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type ForecastItem struct {
	Weather                       string      `json:"weather"`
	Cloud                         string      `json:"cloud"`
	Temperature                   string      `json:"temperature"`
	ApparentTemperature           interface{} `json:"apparent_temperature"`
	ApperentTemperature           interface{} `json:"apperent_temperature"`
	KMSApparentTemperature        interface{} `json:"kms_apparent_temperature"`
	AustralianApparentTemperature interface{} `json:"australian_apparent_temperature"`
	Humidity                      string      `json:"humidity"`
	Precipitation                 string      `json:"precipitation"`
	ProbabilityOfPrecipitation    string      `json:"probability_of_precipitation"`
	WindSpeed                     string      `json:"wind_speed"`
	WindDirectionDegree           string      `json:"wind_direction_degree"`
	WindU                         string      `json:"wind_u"`
	WindV                         string      `json:"wind_v"`
}

var forecastCache sync.Map // cacheID -> map[string]ForecastItem

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

	var resWrapper struct {
		Forecasts map[string]ForecastItem `json:"forecasts"`
	}
	if err := json.Unmarshal(bodyBytes, &resWrapper); err == nil && len(resWrapper.Forecasts) > 0 {
		return resWrapper.Forecasts, nil
	}

	var directMap map[string]ForecastItem
	if err := json.Unmarshal(bodyBytes, &directMap); err == nil {
		return directMap, nil
	}

	return nil, fmt.Errorf("failed to parse forecast JSON response")
}

func formatApparentTemp(item ForecastItem) string {
	parseVal := func(v interface{}) float64 {
		if v == nil {
			return 0
		}
		switch t := v.(type) {
		case float64:
			return t
		case string:
			var f float64
			_, _ = fmt.Sscanf(t, "%f", &f)
			return f
		default:
			return 0
		}
	}

	kmsVal := parseVal(item.KMSApparentTemperature)
	ausVal := parseVal(item.AustralianApparentTemperature)
	defaultVal := parseVal(item.ApparentTemperature)
	if defaultVal == 0 {
		defaultVal = parseVal(item.ApperentTemperature)
	}

	var parts []string
	if kmsVal != 0 {
		parts = append(parts, fmt.Sprintf("기상청 %.1f°C", kmsVal))
	}
	if ausVal != 0 {
		parts = append(parts, fmt.Sprintf("호주식 %.1f°C", ausVal))
	}
	if len(parts) == 0 && defaultVal != 0 {
		parts = append(parts, fmt.Sprintf("%.1f°C", defaultVal))
	}

	return strings.Join(parts, " / ")
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
	var maxPOP int
	hasPOP := false

	weatherCounts := make(map[string]int)
	rainSnow := ""

	for _, k := range items {
		item := forecastMap[k]

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

		if item.ProbabilityOfPrecipitation != "" {
			var popVal int
			if _, err := fmt.Sscanf(item.ProbabilityOfPrecipitation, "%d", &popVal); err == nil {
				if !hasPOP || popVal > maxPOP {
					maxPOP = popVal
					hasPOP = true
				}
			}
		}

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

	var parts []string
	parts = append(parts, fmt.Sprintf("%s **%s**", repEmoji, repWeather))

	if hasTemp {
		if minTemp == maxTemp {
			parts = append(parts, fmt.Sprintf("🌡️ **%.0f°C**", maxTemp))
		} else {
			parts = append(parts, fmt.Sprintf("🌡️ **%.0f°C / %.0f°C**", minTemp, maxTemp))
		}
	}

	if hasPOP {
		parts = append(parts, fmt.Sprintf("🌧️ 강수확률 **%d%%**", maxPOP))
	}

	return dateLabel, strings.Join(parts, " | ")
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

		cacheKey := fmt.Sprintf("%s:%d:%d", pos.Address, pos.X, pos.Y)
		forecastCache.Store(cacheKey, forecastMap)

		var keys []string
		for k := range forecastMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)

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

		button := discordgo.Button{
			Label:    "🔍 상세보기",
			Style:    discordgo.PrimaryButton,
			CustomID: fmt.Sprintf("forecast_detail:%s", cacheKey),
		}

		components := []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{button},
			},
		}

		embeds := []*discordgo.MessageEmbed{embed}
		_, editErr := s.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{
			Embeds:     &embeds,
			Components: &components,
		})
		if editErr != nil {
			slog.Error("Failed to edit interaction response for /forecast", "error", editErr)
		}
	}()
}

func handleForecastDetailComponent(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	customID := ic.MessageComponentData().CustomID

	var cacheKey string
	pageIndex := 0

	if strings.HasPrefix(customID, "forecast_page:") {
		payload := strings.TrimPrefix(customID, "forecast_page:")
		parts := strings.Split(payload, ":")
		if len(parts) >= 4 {
			cacheKey = fmt.Sprintf("%s:%s:%s", parts[0], parts[1], parts[2])
			_, _ = fmt.Sscanf(parts[3], "%d", &pageIndex)
		} else {
			cacheKey = payload
		}
	} else {
		cacheKey = strings.TrimPrefix(customID, "forecast_detail:")
	}

	parts := strings.Split(cacheKey, ":")
	var pos *LocationPos
	if len(parts) >= 3 {
		var x, y int
		_, _ = fmt.Sscanf(parts[1], "%d", &x)
		_, _ = fmt.Sscanf(parts[2], "%d", &y)
		pos = &LocationPos{
			Address: parts[0],
			X:       x,
			Y:       y,
		}
	} else {
		pos = getLocationCoordinates(db, "")
	}

	var forecastMap map[string]ForecastItem
	if val, ok := forecastCache.Load(cacheKey); ok {
		if m, ok := val.(map[string]ForecastItem); ok {
			forecastMap = m
		}
	}

	if len(forecastMap) == 0 {
		var err error
		forecastMap, err = fetchForecastInfo(pos)
		if err != nil {
			respData := &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("상세 예보 정보를 가져오는데 실패했습니다: %v", err),
			}
			if strings.HasPrefix(customID, "forecast_page:") {
				_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseUpdateMessage,
					Data: respData,
				})
			} else {
				_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: respData,
				})
			}
			return
		}
	}

	var keys []string
	for k := range forecastMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)

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

	totalPages := len(dateOrder)
	if totalPages == 0 {
		return
	}

	if pageIndex < 0 {
		pageIndex = 0
	}
	if pageIndex >= totalPages {
		pageIndex = totalPages - 1
	}

	targetDateStr := dateOrder[pageIndex]
	targetItems := dateMap[targetDateStr]

	t, err := time.Parse("20060102", targetDateStr)
	var dateLabel string
	if err == nil {
		weekdays := []string{"일", "월", "화", "수", "목", "금", "토"}
		dateLabel = fmt.Sprintf("%s (%s)", t.Format("2006년 01월 02일"), weekdays[t.Weekday()])
	} else {
		dateLabel = targetDateStr
	}

	var amLines []string
	var pmLines []string

	for _, k := range targetItems {
		parts := strings.Split(k, "-")
		if len(parts) != 2 || len(parts[1]) != 4 {
			continue
		}
		timeStr := parts[1]
		hourMinStr := fmt.Sprintf("%s:%s", timeStr[:2], timeStr[2:])
		var hourVal int
		_, _ = fmt.Sscanf(timeStr[:2], "%d", &hourVal)

		item := forecastMap[k]

		wEmoji := getWeatherEmoji(item.Weather)
		if item.Weather == "" {
			wEmoji = getWeatherEmoji(item.Cloud)
		}
		wName := item.Weather
		if wName == "" {
			wName = item.Cloud
		}

		tempStr := item.Temperature
		if tempStr != "" {
			tempStr = tempStr + "°C"
		} else {
			tempStr = "-"
		}

		appTempStr := formatApparentTemp(item)
		popStr := item.ProbabilityOfPrecipitation
		if popStr != "" {
			popStr = popStr + "%"
		} else {
			popStr = "0%"
		}

		precipStr := item.Precipitation
		if precipStr == "" || precipStr == "0" || precipStr == "강수없음" {
			precipStr = "강수 없음"
		} else if !strings.HasSuffix(precipStr, "mm") {
			precipStr = precipStr + "mm"
		}

		var precipLine string
		if precipStr == "강수 없음" {
			precipLine = fmt.Sprintf("🌧️ 강수 없음 (확률 %s)", popStr)
		} else {
			precipLine = fmt.Sprintf("🌧️ 강수량 %s (확률 %s)", precipStr, popStr)
		}

		var entry string
		if appTempStr != "" {
			entry = fmt.Sprintf("• `%s` %s **%s** | 🌡️ **%s**\n  └ 체감: %s\n  └ 💧 습도 %s%% | %s",
				hourMinStr, wEmoji, wName, tempStr, appTempStr, item.Humidity, precipLine)
		} else {
			entry = fmt.Sprintf("• `%s` %s **%s** | 🌡️ **%s**\n  └ 💧 습도 %s%% | %s",
				hourMinStr, wEmoji, wName, tempStr, item.Humidity, precipLine)
		}

		if hourVal < 12 {
			amLines = append(amLines, entry)
		} else {
			pmLines = append(pmLines, entry)
		}
	}

	embed := &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("📊 시간별 상세 단기예보 (%d/%d 일차)", pageIndex+1, totalPages),
		Description: fmt.Sprintf("📍 위치: **%s** (격자: %d, %d)\n📅 **%s** (%d개 시간대)", pos.Address, pos.X, pos.Y, dateLabel, len(targetItems)),
		Color:       0x2ecc71,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		Footer: &discordgo.MessageEmbedFooter{
			Text: "기상청 단기예보 기준",
		},
	}

	if len(amLines) > 0 {
		valStr := strings.Join(amLines, "\n\n")
		runes := []rune(valStr)
		if len(runes) > 1000 {
			valStr = string(runes[:990]) + "\n..."
		}
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "🌅 오전 예보 (00:00 ~ 11:00)",
			Value:  valStr,
			Inline: false,
		})
	}

	if len(pmLines) > 0 {
		valStr := strings.Join(pmLines, "\n\n")
		runes := []rune(valStr)
		if len(runes) > 1000 {
			valStr = string(runes[:990]) + "\n..."
		}
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "🌆 오후 예보 (12:00 ~ 23:00)",
			Value:  valStr,
			Inline: false,
		})
	}

	// Pagination Navigation Buttons
	btnPrev := discordgo.Button{
		Label:    "◀ 이전 일자",
		Style:    discordgo.PrimaryButton,
		CustomID: fmt.Sprintf("forecast_page:%s:%d", cacheKey, pageIndex-1),
		Disabled: (pageIndex == 0),
	}

	btnIndicator := discordgo.Button{
		Label:    fmt.Sprintf("%d / %d 일차", pageIndex+1, totalPages),
		Style:    discordgo.SecondaryButton,
		CustomID: "forecast_page_indicator",
		Disabled: true,
	}

	btnNext := discordgo.Button{
		Label:    "다음 일자 ▶",
		Style:    discordgo.PrimaryButton,
		CustomID: fmt.Sprintf("forecast_page:%s:%d", cacheKey, pageIndex+1),
		Disabled: (pageIndex >= totalPages-1),
	}

	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{btnPrev, btnIndicator, btnNext},
		},
	}

	responseData := &discordgo.InteractionResponseData{
		Embeds:     []*discordgo.MessageEmbed{embed},
		Components: components,
	}

	if strings.HasPrefix(customID, "forecast_page:") {
		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: responseData,
		})
	} else {
		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: responseData,
		})
	}
}
