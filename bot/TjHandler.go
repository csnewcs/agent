package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// --- Command Definition ---

func buildTJCommand() (BotCommand, error) {
	return NewBotCommandBuilder("tj").
		WithDescription(".").
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionSubCommand,
			Name:        "search",
			Description: ".",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "query",
					Description: ".",
					Required:    true,
				},
			},
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionSubCommand,
			Name:        "add",
			Description: ".",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "category",
					Description: ".",
					Required:    true,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "song (곡 제목)", Value: "song"},
						{Name: "artist (아티스트)", Value: "artist"},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "name",
					Description: ".",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "alias",
					Description: ".",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionBoolean,
					Name:        "auto_search",
					Description: ".",
					Required:    false,
				},
			},
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionSubCommand,
			Name:        "delete",
			Description: ".",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "category",
					Description: ".",
					Required:    true,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "song (곡 제목)", Value: "song"},
						{Name: "artist (아티스트)", Value: "artist"},
					},
				},
				{
					Type:         discordgo.ApplicationCommandOptionString,
					Name:         "name",
					Description:  ".",
					Required:     true,
					Autocomplete: true,
				},
			},
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionSubCommand,
			Name:        "list",
			Description: ".",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "category",
					Description: ".",
					Required:    false,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "all (전체 현황)", Value: "all"},
						{Name: "song (곡 목록)", Value: "song"},
						{Name: "artist (아티스트 목록)", Value: "artist"},
						{Name: "matches (오늘 매칭된 신곡)", Value: "matches"},
					},
				},
			},
		}).
		WithFunction(handleTJCommand).
		Build()
}

// --- Data Structures Matching DISCORD_BOT_SPEC.md ---

type TJSearchResult struct {
	Query      string        `json:"query"`
	Candidates []TJCandidate `json:"candidates"`
	Error      string        `json:"error,omitempty"`
}

type TJCandidate struct {
	Title       string   `json:"title"`
	Artist      string   `json:"artist,omitempty"`
	Source      string   `json:"source"`
	AltTitles   []string `json:"alt_titles"`
	Description string   `json:"description,omitempty"`
}

type TJListJSONOutput struct {
	TodayMatches []TJMatchedHistoryRecord `json:"today_matches"`
	Artists      []TJTargetWithAlts       `json:"artists"`
	Songs        []TJTargetWithAlts       `json:"songs"`
}

type TJMatchedHistoryRecord struct {
	ID          int       `json:"ID"`
	Pro         int       `json:"Pro"`
	Title       string    `json:"Title"`
	Artist      string    `json:"Artist"`
	PublishDate time.Time `json:"PublishDate"`
	MatchedAt   time.Time `json:"MatchedAt"`
}

type TJTargetWithAlts struct {
	Title     string   `json:"title"`
	AltTitles []string `json:"alt_titles"`
	StartFrom string   `json:"start_from"`
}

// --- Database & CLI Client ---

type TJDBClient struct {
	*sql.DB
}

var tjDB *TJDBClient

func InitTJDB(cfg *Config) error {
	dbURL := cfg.TJDBURL
	if dbURL == "" {
		dbURL = "postgresql://tj@localhost:5432/tj?sslmode=disable"
	}
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return err
	}
	if err := db.Ping(); err != nil {
		// Even if direct DB fails initially, CLI might still work
		slog.Warn("TJ DB direct ping failed (will try CLI)", "url", dbURL, "error", err)
	} else {
		slog.Info("Successfully connected to TJ database", "url", dbURL)
	}
	tjDB = &TJDBClient{db}
	return nil
}

func findTrackingTJBinary() string {
	candidates := []string{
		os.Getenv("TRACKING_TJ_BIN"),
		"/TrackingTJNewSongs/tracking-tj",
		"/home/fedora/git/TrackingTJNewSongs/tracking-tj",
		"./tracking-tj",
		"tracking-tj",
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, err := os.Stat(c); err == nil {
			return c
		}
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

func runTrackingTJCLI(args ...string) (string, error) {
	bin := findTrackingTJBinary()
	if bin == "" {
		return "", fmt.Errorf("tracking-tj binary not found")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	// Set working directory if binary is in git repo
	if strings.Contains(bin, "TrackingTJNewSongs") {
		cmd.Dir = "/home/fedora/git/TrackingTJNewSongs"
		if _, err := os.Stat(cmd.Dir); err != nil {
			cmd.Dir = "/TrackingTJNewSongs"
		}
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("tracking-tj execution failed (%v): %s", err, string(out))
	}
	return string(out), nil
}

// --- High Level Operations (CLI with DB Fallback) ---

func SearchTJAliases(query string) (*TJSearchResult, error) {
	// 1. Try CLI
	if bin := findTrackingTJBinary(); bin != "" {
		out, err := runTrackingTJCLI("search", query)
		if err == nil && out != "" {
			var res TJSearchResult
			if err := json.Unmarshal([]byte(out), &res); err == nil && res.Error == "" {
				return &res, nil
			}
		}
	}

	// 2. Direct Search Fallback (YouTube + Namuwiki scraper)
	return directSearchAliases(query)
}

func AddTJTracking(category, name string, aliases []string, autoSearch bool) (string, error) {
	category = strings.ToLower(strings.TrimSpace(category))
	name = strings.TrimSpace(name)

	// 1. Try CLI
	if bin := findTrackingTJBinary(); bin != "" {
		args := []string{"add", category, name}
		if autoSearch {
			args = append(args, "--auto-search")
		} else if len(aliases) > 0 {
			args = append(args, "--alias="+strings.Join(aliases, ","))
		}
		out, err := runTrackingTJCLI(args...)
		if err == nil {
			return strings.TrimSpace(out), nil
		}
		slog.Warn("AddTJTracking CLI failed, falling back to direct DB", "error", err)
	}

	// 2. Direct DB Fallback
	if tjDB == nil || tjDB.DB == nil {
		return "", fmt.Errorf("데이터베이스 및 CLI를 사용할 수 없습니다.")
	}

	// Collect aliases if auto-search requested
	if autoSearch {
		if sRes, err := directSearchAliases(name); err == nil {
			for _, c := range sRes.Candidates {
				aliases = append(aliases, c.AltTitles...)
			}
		}
	}

	var table string
	if category == "artist" {
		table = "tracking_artists"
	} else if category == "song" {
		table = "tracking_songs"
	} else {
		return "", fmt.Errorf("잘못된 구분입니다: %s", category)
	}

	// Check if already exists
	var count int
	_ = tjDB.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE title = $1", table), name).Scan(&count)
	if count == 0 {
		_, err := tjDB.Exec(fmt.Sprintf("INSERT INTO %s (title, start_from) VALUES ($1, CURRENT_DATE)", table), name)
		if err != nil {
			return "", fmt.Errorf("트래킹 추가 실패: %w", err)
		}
	}

	// Add alt_titles
	if len(aliases) > 0 {
		for _, alt := range aliases {
			alt = strings.TrimSpace(alt)
			if alt == "" || strings.EqualFold(alt, name) {
				continue
			}
			_, _ = tjDB.Exec(
				"INSERT INTO alt_titles (target_type, target_title, alt_title, source) VALUES ($1, $2, $3, $4) ON CONFLICT (target_type, target_title, alt_title) DO NOTHING",
				category, name, alt, "manual",
			)
		}
	}

	var formattedAlts string
	if len(aliases) > 0 {
		formattedAlts = strings.Join(aliases, ", ")
	} else {
		formattedAlts = "(없음)"
	}

	if category == "artist" {
		return fmt.Sprintf("관심 아티스트 추가 완료: '%s' (별칭: %s)", name, formattedAlts), nil
	}
	return fmt.Sprintf("관심 곡 추가 완료: '%s' (별칭: %s)", name, formattedAlts), nil
}

func DeleteTJTracking(category, name string) (string, error) {
	category = strings.ToLower(strings.TrimSpace(category))
	name = strings.TrimSpace(name)

	// 1. Try CLI
	if bin := findTrackingTJBinary(); bin != "" {
		out, err := runTrackingTJCLI("delete", category, name)
		if err == nil {
			return strings.TrimSpace(out), nil
		}
		slog.Warn("DeleteTJTracking CLI failed, falling back to direct DB", "error", err)
	}

	// 2. Direct DB Fallback
	if tjDB == nil || tjDB.DB == nil {
		return "", fmt.Errorf("데이터베이스 및 CLI를 사용할 수 없습니다.")
	}

	var table string
	if category == "artist" {
		table = "tracking_artists"
	} else if category == "song" {
		table = "tracking_songs"
	} else {
		return "", fmt.Errorf("잘못된 구분입니다: %s", category)
	}

	// Delete alt_titles
	_, _ = tjDB.Exec("DELETE FROM alt_titles WHERE target_type = $1 AND target_title = $2", category, name)

	res, err := tjDB.Exec(fmt.Sprintf("DELETE FROM %s WHERE title = $1", table), name)
	if err != nil {
		return "", fmt.Errorf("트래킹 삭제 실패: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Sprintf("삭제 대상 '%s'를 찾을 수 없습니다.", name), nil
	}

	if category == "artist" {
		return fmt.Sprintf("관심 아티스트가 삭제되었습니다: '%s'", name), nil
	}
	return fmt.Sprintf("관심 곡이 삭제되었습니다: '%s'", name), nil
}

func ListTJTrackingJSON() (*TJListJSONOutput, error) {
	// 1. Try CLI
	if bin := findTrackingTJBinary(); bin != "" {
		out, err := runTrackingTJCLI("list", "--json")
		if err == nil && out != "" {
			var listOut TJListJSONOutput
			if err := json.Unmarshal([]byte(out), &listOut); err == nil {
				return &listOut, nil
			}
		}
	}

	// 2. Direct DB Fallback
	if tjDB == nil || tjDB.DB == nil {
		return nil, fmt.Errorf("데이터베이스 및 CLI를 사용할 수 없습니다.")
	}

	out := &TJListJSONOutput{
		TodayMatches: []TJMatchedHistoryRecord{},
		Artists:      []TJTargetWithAlts{},
		Songs:        []TJTargetWithAlts{},
	}

	// Today matches
	mRows, err := tjDB.Query("SELECT id, pro, title, artist, publish_date, matched_at FROM matched_history WHERE publish_date = CURRENT_DATE OR matched_at::date = CURRENT_DATE ORDER BY matched_at DESC")
	if err == nil {
		defer mRows.Close()
		for mRows.Next() {
			var m TJMatchedHistoryRecord
			if err := mRows.Scan(&m.ID, &m.Pro, &m.Title, &m.Artist, &m.PublishDate, &m.MatchedAt); err == nil {
				out.TodayMatches = append(out.TodayMatches, m)
			}
		}
	}

	// Alt titles maps
	artistAltMap := make(map[string][]string)
	songAltMap := make(map[string][]string)
	altRows, err := tjDB.Query("SELECT target_type, target_title, alt_title FROM alt_titles")
	if err == nil {
		defer altRows.Close()
		for altRows.Next() {
			var tType, tTitle, aTitle string
			if err := altRows.Scan(&tType, &tTitle, &aTitle); err == nil {
				if tType == "artist" {
					artistAltMap[tTitle] = append(artistAltMap[tTitle], aTitle)
				} else if tType == "song" {
					songAltMap[tTitle] = append(songAltMap[tTitle], aTitle)
				}
			}
		}
	}

	// Artists
	aRows, err := tjDB.Query("SELECT title, COALESCE(start_from::text, '') FROM tracking_artists ORDER BY start_from DESC, title ASC")
	if err == nil {
		defer aRows.Close()
		for aRows.Next() {
			var item TJTargetWithAlts
			if err := aRows.Scan(&item.Title, &item.StartFrom); err == nil {
				item.AltTitles = artistAltMap[item.Title]
				out.Artists = append(out.Artists, item)
			}
		}
	}

	// Songs
	sRows, err := tjDB.Query("SELECT title, COALESCE(start_from::text, '') FROM tracking_songs ORDER BY start_from DESC, title ASC")
	if err == nil {
		defer sRows.Close()
		for sRows.Next() {
			var item TJTargetWithAlts
			if err := sRows.Scan(&item.Title, &item.StartFrom); err == nil {
				item.AltTitles = songAltMap[item.Title]
				out.Songs = append(out.Songs, item)
			}
		}
	}

	return out, nil
}

// --- Direct Web Scraper Fallback for Search ---

func directSearchAliases(query string) (*TJSearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("검색어가 비어있습니다.")
	}

	result := &TJSearchResult{
		Query:      query,
		Candidates: []TJCandidate{},
	}

	client := &http.Client{Timeout: 8 * time.Second}

	// 1. YouTube Search
	searchURL := fmt.Sprintf("https://www.youtube.com/results?search_query=%s", url.QueryEscape(query))
	req, err := http.NewRequest("GET", searchURL, nil)
	if err == nil {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
		req.Header.Set("Accept-Language", "ko-KR,ko;q=0.9,ja;q=0.8,en;q=0.7")
		if resp, err := client.Do(req); err == nil {
			defer resp.Body.Close()
			if b, err := io.ReadAll(resp.Body); err == nil {
				titleRegex := regexp.MustCompile(`"title":\{"runs":\[\{"text":"([^"]+)"`)
				matches := titleRegex.FindAllStringSubmatch(string(b), 10)
				seen := make(map[string]bool)
				for _, m := range matches {
					if len(m) >= 2 {
						t := strings.TrimSpace(m[1])
						if t != "" && !seen[t] {
							seen[t] = true
							alts := extractSubTitles(t)
							result.Candidates = append(result.Candidates, TJCandidate{
								Title:       t,
								Source:      "youtube",
								AltTitles:   alts,
								Description: "YouTube 검색 영상/음원 제목",
							})
							if len(result.Candidates) >= 3 {
								break
							}
						}
					}
				}
			}
		}
	}

	// 2. Namuwiki Search
	docURL := fmt.Sprintf("https://namu.wiki/w/%s", url.PathEscape(query))
	if req, err := http.NewRequest("GET", docURL, nil); err == nil {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
		req.Header.Set("Accept-Language", "ko-KR,ko;q=0.9,ja;q=0.8,en;q=0.7")
		if resp, err := client.Do(req); err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == 200 {
				if b, err := io.ReadAll(resp.Body); err == nil {
					ogRegex := regexp.MustCompile(`<meta property="og:title" content="([^"]+)"`)
					m := ogRegex.FindStringSubmatch(string(b))
					if len(m) >= 2 {
						docTitle := strings.TrimSuffix(m[1], " - 나무위키")
						docTitle = strings.TrimSpace(docTitle)
						alts := extractSubTitles(docTitle)
						if !strings.EqualFold(docTitle, query) {
							alts = append(alts, docTitle)
						}
						result.Candidates = append(result.Candidates, TJCandidate{
							Title:       docTitle,
							Source:      "namuwiki",
							AltTitles:   alts,
							Description: "나무위키 문서: " + docURL,
						})
					}
				}
			}
		}
	}

	return result, nil
}

func extractSubTitles(title string) []string {
	var results []string
	seen := make(map[string]bool)

	bracketRegex := regexp.MustCompile(`[\(\[\{【『「]([^\)\]\}】』」]+)[\)\]\}】』」]`)
	for _, m := range bracketRegex.FindAllStringSubmatch(title, -1) {
		if len(m) >= 2 {
			sub := strings.TrimSpace(m[1])
			if sub != "" && !seen[sub] && !isJunkTitle(sub) {
				seen[sub] = true
				results = append(results, sub)
			}
		}
	}

	for _, part := range strings.Split(title, "/") {
		part = strings.TrimSpace(bracketRegex.ReplaceAllString(part, ""))
		if part != "" && !seen[part] && !isJunkTitle(part) {
			seen[part] = true
			results = append(results, part)
		}
	}

	cleaned := strings.TrimSpace(bracketRegex.ReplaceAllString(title, ""))
	if cleaned != "" && !seen[cleaned] && !isJunkTitle(cleaned) {
		seen[cleaned] = true
		results = append(results, cleaned)
	}

	return results
}

func isJunkTitle(s string) bool {
	low := strings.ToLower(s)
	junk := []string{"official", "mv", "music video", "audio", "full", "ver", "cover", "lyrics", "ft.", "feat", "영상", "음원", "가사", "공식", "자막"}
	for _, j := range junk {
		if strings.Contains(low, j) {
			return true
		}
	}
	return len([]rune(s)) < 2
}

// --- Slash Command & Autocomplete Handlers ---

func handleTJCommand(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	options := ic.ApplicationCommandData().Options
	if len(options) == 0 {
		return
	}

	subCmd := options[0]
	switch subCmd.Name {
	case "search":
		handleTJSearch(s, ic, subCmd.Options)
	case "add":
		handleTJAdd(s, ic, subCmd.Options)
	case "delete":
		handleTJDelete(s, ic, subCmd.Options)
	case "list":
		handleTJList(s, ic, subCmd.Options)
	}
}

func handleTJSearch(s *discordgo.Session, ic *discordgo.InteractionCreate, options []*discordgo.ApplicationCommandInteractionDataOption) {
	var query string
	for _, opt := range options {
		if opt.Name == "query" {
			query = strings.TrimSpace(opt.StringValue())
		}
	}

	if query == "" {
		comps := SimpleErrorCard("검색어(query)를 입력해 주세요.")
		_ = RespondComponentsV2(s, ic, comps, true)
		return
	}

	// Defer response
	_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	searchRes, err := SearchTJAliases(query)
	if err != nil {
		slog.Error("Failed to search TJ aliases", "query", query, "error", err)
		errMsg := fmt.Sprintf("별칭 검색 중 오류가 발생했습니다: %v", err)
		_, _ = s.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{
			Content: &errMsg,
		})
		return
	}

	var allAlts []string
	altSeen := make(map[string]bool)
	var candidateLines []string

	for i, c := range searchRes.Candidates {
		sourceBadge := "YouTube"
		if c.Source == "namuwiki" {
			sourceBadge = "나무위키"
		}
		candidateLines = append(candidateLines, fmt.Sprintf("**%d. %s** [%s]", i+1, c.Title, sourceBadge))
		for _, a := range c.AltTitles {
			if !altSeen[strings.ToLower(a)] && !strings.EqualFold(a, query) {
				altSeen[strings.ToLower(a)] = true
				allAlts = append(allAlts, a)
			}
		}
	}

	var bodyParts []string
	bodyParts = append(bodyParts, fmt.Sprintf("검색어: **%s**", query))

	if len(allAlts) > 0 {
		var pillList []string
		for _, a := range allAlts {
			pillList = append(pillList, fmt.Sprintf("`%s`", a))
		}
		bodyParts = append(bodyParts, fmt.Sprintf("**추천 별칭 및 번역명 (%d개)**\n%s", len(allAlts), strings.Join(pillList, " ")))
	} else {
		bodyParts = append(bodyParts, "**추천 별칭**: _(별칭 후보가 발견되지 않았습니다)_")
	}

	if len(candidateLines) > 0 {
		bodyParts = append(bodyParts, fmt.Sprintf("**검색된 원본 문서/영상**\n%s", strings.Join(candidateLines, "\n")))
	}

	// Quick Add Buttons
	encodedQuery := url.QueryEscape(query)
	btnSong := discordgo.Button{
		Label:    "관심 곡으로 등록",
		Style:    discordgo.PrimaryButton,
		CustomID: "tj_add_song:" + encodedQuery,
	}
	btnArtist := discordgo.Button{
		Label:    "관심 아티스트로 등록",
		Style:    discordgo.SecondaryButton,
		CustomID: "tj_add_artist:" + encodedQuery,
	}

	comps := NewComponentsBuilder().
		WithTitle("TJ 노래방 별칭 / 번역명 검색 결과").
		WithBody(strings.Join(bodyParts, "\n\n")).
		WithFooter("버튼 클릭 시 발견된 추천 별칭과 함께 자동으로 등록됩니다. • TJ Media").
		WithButtons(btnSong, btnArtist).
		Build()

	_, _ = EditInteractionComponentsV2(s, ic, comps)
}

func handleTJAdd(s *discordgo.Session, ic *discordgo.InteractionCreate, options []*discordgo.ApplicationCommandInteractionDataOption) {
	var category, name, aliasStr string
	var autoSearch bool

	for _, opt := range options {
		switch opt.Name {
		case "category":
			category = opt.StringValue()
		case "name":
			name = opt.StringValue()
		case "alias":
			aliasStr = opt.StringValue()
		case "auto_search":
			autoSearch = opt.BoolValue()
		}
	}

	if category == "" || name == "" {
		comps := SimpleErrorCard("구분(category)과 대상 이름(name)을 입력해 주세요.")
		_ = RespondComponentsV2(s, ic, comps, true)
		return
	}

	var aliases []string
	if aliasStr != "" {
		for _, a := range strings.Split(aliasStr, ",") {
			if t := strings.TrimSpace(a); t != "" {
				aliases = append(aliases, t)
			}
		}
	}

	// If no aliases provided and auto_search not explicitly false, auto search is useful
	_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	resultMsg, err := AddTJTracking(category, name, aliases, autoSearch)
	if err != nil {
		slog.Error("Failed to add TJ tracking", "category", category, "name", name, "error", err)
		comps := SimpleErrorCard(fmt.Sprintf("트래킹 등록 중 오류가 발생했습니다: %v", err))
		_, _ = EditInteractionComponentsV2(s, ic, comps)
		return
	}

	var catLabel string
	if category == "artist" {
		catLabel = "아티스트"
	} else {
		catLabel = "곡 제목"
	}

	todayStr := time.Now().Format("2006-01-02")
	body := fmt.Sprintf("• **구분**: %s\n• **결과**: %s\n• **등록일**: %s", catLabel, resultMsg, todayStr)

	comps := NewComponentsBuilder().
		WithTitle("TJ 노래방 트래킹 등록 완료").
		WithBody(body).
		WithFooter("신곡 출시 시 디스코드 자동 알림이 발송됩니다. • TJ Media").
		Build()

	_, _ = EditInteractionComponentsV2(s, ic, comps)
}

func handleTJDelete(s *discordgo.Session, ic *discordgo.InteractionCreate, options []*discordgo.ApplicationCommandInteractionDataOption) {
	var category, name string
	for _, opt := range options {
		if opt.Name == "category" {
			category = opt.StringValue()
		} else if opt.Name == "name" {
			name = opt.StringValue()
		}
	}

	if category == "" || name == "" {
		comps := SimpleErrorCard("구분(category)과 대상 이름(name)을 입력해 주세요.")
		_ = RespondComponentsV2(s, ic, comps, true)
		return
	}

	_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	resultMsg, err := DeleteTJTracking(category, name)
	if err != nil {
		slog.Error("Failed to delete TJ tracking", "category", category, "name", name, "error", err)
		comps := SimpleErrorCard(fmt.Sprintf("트래킹 삭제 중 오류가 발생했습니다: %v", err))
		_, _ = EditInteractionComponentsV2(s, ic, comps)
		return
	}

	var catLabel string
	if category == "artist" {
		catLabel = "아티스트"
	} else {
		catLabel = "곡 제목"
	}

	comps := NewComponentsBuilder().
		WithTitle("TJ 노래방 트래킹 삭제 완료").
		WithBody(fmt.Sprintf("• **구분**: %s\n• **결과**: %s\n• **안내**: 연결된 모든 별칭도 함께 삭제되었습니다.", catLabel, resultMsg)).
		WithFooter("트래킹 목록에서 제거되었습니다. • TJ Media").
		Build()

	_, _ = EditInteractionComponentsV2(s, ic, comps)
}

func handleTJList(s *discordgo.Session, ic *discordgo.InteractionCreate, options []*discordgo.ApplicationCommandInteractionDataOption) {
	category := "all"
	for _, opt := range options {
		if opt.Name == "category" {
			category = opt.StringValue()
		}
	}

	_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	listData, err := ListTJTrackingJSON()
	if err != nil {
		slog.Error("Failed to list TJ tracking", "error", err)
		comps := SimpleErrorCard(fmt.Sprintf("트래킹 목록 조회 중 오류가 발생했습니다: %v", err))
		_, _ = EditInteractionComponentsV2(s, ic, comps)
		return
	}

	var bodyParts []string

	// 1. Today Matches (Always show if category is all or matches)
	if category == "all" || category == "matches" {
		if len(listData.TodayMatches) > 0 {
			var matchLines []string
			for _, m := range listData.TodayMatches {
				matchLines = append(matchLines, fmt.Sprintf("• **[%d]** **%s** - %s `(수록: %s | 확인: %s)`",
					m.Pro, m.Title, m.Artist, m.PublishDate.Format("2006-01-02"), m.MatchedAt.Format("15:04:05")))
			}
			bodyParts = append(bodyParts, fmt.Sprintf("**오늘 매칭된 신곡 (%d곡)**\n%s", len(listData.TodayMatches), strings.Join(matchLines, "\n")))
		} else if category == "matches" {
			bodyParts = append(bodyParts, "**오늘 매칭된 신곡**\n_(오늘 등록 확인된 신곡이 없습니다)_")
		}
	}

	// 2. Artists
	if category == "all" || category == "artist" {
		var artistStr string
		if len(listData.Artists) == 0 {
			artistStr = "_(등록된 트래킹 아티스트가 없습니다)_"
		} else {
			var lines []string
			for _, a := range listData.Artists {
				if len(a.AltTitles) > 0 {
					lines = append(lines, fmt.Sprintf("• **%s** `(별칭: %s | 시작: %s)`", a.Title, strings.Join(a.AltTitles, ", "), a.StartFrom))
				} else {
					lines = append(lines, fmt.Sprintf("• **%s** `(시작: %s)`", a.Title, a.StartFrom))
				}
			}
			artistStr = strings.Join(lines, "\n")
		}

		if len([]rune(artistStr)) > 1200 {
			artistStr = string([]rune(artistStr)[:1190]) + "\n... (외 다수)"
		}
		bodyParts = append(bodyParts, fmt.Sprintf("**트래킹 아티스트 (%d명)**\n%s", len(listData.Artists), artistStr))
	}

	// 3. Songs
	if category == "all" || category == "song" {
		var songStr string
		if len(listData.Songs) == 0 {
			songStr = "_(등록된 트래킹 곡이 없습니다)_"
		} else {
			var lines []string
			for _, sg := range listData.Songs {
				if len(sg.AltTitles) > 0 {
					lines = append(lines, fmt.Sprintf("• **%s** `(별칭: %s | 시작: %s)`", sg.Title, strings.Join(sg.AltTitles, ", "), sg.StartFrom))
				} else {
					lines = append(lines, fmt.Sprintf("• **%s** `(시작: %s)`", sg.Title, sg.StartFrom))
				}
			}
			songStr = strings.Join(lines, "\n")
		}

		if len([]rune(songStr)) > 1200 {
			songStr = string([]rune(songStr)[:1190]) + "\n... (외 다수)"
		}
		bodyParts = append(bodyParts, fmt.Sprintf("**트래킹 곡 (%d곡)**\n%s", len(listData.Songs), songStr))
	}

	comps := NewComponentsBuilder().
		WithTitle("TJ 노래방 트래킹 현황").
		WithBody(strings.Join(bodyParts, "\n\n")).
		WithFooter("신곡 업데이트 시 디스코드 자동 알림 발송 • TJ Media").
		Build()

	_, _ = EditInteractionComponentsV2(s, ic, comps)
}

// SendTJDeleteAutocomplete handles autocomplete for delete command
func SendTJDeleteAutocomplete(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	data := ic.ApplicationCommandData()
	var category, currentInput string

	for _, opt := range data.Options {
		if opt.Type == discordgo.ApplicationCommandOptionSubCommand && opt.Name == "delete" {
			for _, subOpt := range opt.Options {
				if subOpt.Name == "category" {
					category = subOpt.StringValue()
				} else if subOpt.Name == "name" && subOpt.Focused {
					currentInput = strings.TrimSpace(subOpt.StringValue())
				}
			}
		}
	}

	listData, err := ListTJTrackingJSON()
	if err != nil || listData == nil {
		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionApplicationCommandAutocompleteResult,
			Data: &discordgo.InteractionResponseData{
				Choices: []*discordgo.ApplicationCommandOptionChoice{},
			},
		})
		return
	}

	var candidates []string
	if category == "artist" {
		for _, a := range listData.Artists {
			candidates = append(candidates, a.Title)
		}
	} else if category == "song" {
		for _, s := range listData.Songs {
			candidates = append(candidates, s.Title)
		}
	} else {
		for _, a := range listData.Artists {
			candidates = append(candidates, a.Title)
		}
		for _, s := range listData.Songs {
			candidates = append(candidates, s.Title)
		}
	}

	var choices []*discordgo.ApplicationCommandOptionChoice
	lowInput := strings.ToLower(currentInput)

	for _, c := range candidates {
		if lowInput == "" || strings.Contains(strings.ToLower(c), lowInput) {
			choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
				Name:  c,
				Value: c,
			})
			if len(choices) >= 25 {
				break
			}
		}
	}

	_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionApplicationCommandAutocompleteResult,
		Data: &discordgo.InteractionResponseData{
			Choices: choices,
		},
	})
}

// HandleTJComponent handles button clicks (Quick Add from search)
func HandleTJComponent(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	customID := ic.MessageComponentData().CustomID
	slog.Info("TJ Component interaction received", "custom_id", customID)

	var category, rawQuery string
	if strings.HasPrefix(customID, "tj_add_song:") {
		category = "song"
		rawQuery = strings.TrimPrefix(customID, "tj_add_song:")
	} else if strings.HasPrefix(customID, "tj_add_artist:") {
		category = "artist"
		rawQuery = strings.TrimPrefix(customID, "tj_add_artist:")
	} else {
		return
	}

	query, _ := url.QueryUnescape(rawQuery)
	query = strings.TrimSpace(query)
	if query == "" {
		return
	}

	// Defer update
	_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	resultMsg, err := AddTJTracking(category, query, nil, true)
	if err != nil {
		slog.Error("Failed to add TJ tracking via component", "category", category, "name", query, "error", err)
		comps := SimpleErrorCard(fmt.Sprintf("등록 중 오류가 발생했습니다: %v", err))
		_, _ = EditInteractionComponentsV2(s, ic, comps)
		return
	}

	var catLabel string
	if category == "artist" {
		catLabel = "아티스트"
	} else {
		catLabel = "곡 제목"
	}

	todayStr := time.Now().Format("2006-01-02")
	comps := NewComponentsBuilder().
		WithTitle("TJ 노래방 관심 등록 완료").
		WithBody(fmt.Sprintf("• **구분**: %s\n• **결과**: %s\n• **등록일**: %s", catLabel, resultMsg, todayStr)).
		WithFooter("신곡 출시 시 디스코드 자동 알림이 발송됩니다. • TJ Media").
		Build()

	_, _ = EditInteractionComponentsV2(s, ic, comps)
}
