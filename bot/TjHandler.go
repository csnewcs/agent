package main

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type TJDBClient struct {
	*sql.DB
}

var tjDB *TJDBClient

func InitTJDB(cfg *Config) error {
	db, err := sql.Open("pgx", cfg.TJDBURL)
	if err != nil {
		return err
	}
	if err := db.Ping(); err != nil {
		return err
	}
	tjDB = &TJDBClient{db}
	slog.Info("Successfully connected to TJ database", "url", cfg.TJDBURL)
	return nil
}

type TrackingItem struct {
	Title     string
	StartFrom string
}

func (c *TJDBClient) AddTracking(category string, name string) (bool, error) {
	if c == nil {
		return false, fmt.Errorf("TJ database client is nil")
	}

	name = strings.TrimSpace(name)
	if name == "" {
		return false, fmt.Errorf("name cannot be empty")
	}

	var table string
	if category == "artist" {
		table = "tracking_artists"
	} else if category == "song" {
		table = "tracking_songs"
	} else {
		return false, fmt.Errorf("invalid category: %s", category)
	}

	// Check if already exists
	var count int
	checkQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE title = $1", table)
	err := c.QueryRow(checkQuery, name).Scan(&count)
	if err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil // Already exists
	}

	insertQuery := fmt.Sprintf("INSERT INTO %s (title, start_from) VALUES ($1, CURRENT_DATE)", table)
	_, err = c.Exec(insertQuery, name)
	if err != nil {
		return false, err
	}
	return true, nil
}

func (c *TJDBClient) DeleteTracking(category string, name string) (int64, error) {
	if c == nil {
		return 0, fmt.Errorf("TJ database client is nil")
	}

	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("name cannot be empty")
	}

	var table string
	if category == "artist" {
		table = "tracking_artists"
	} else if category == "song" {
		table = "tracking_songs"
	} else {
		return 0, fmt.Errorf("invalid category: %s", category)
	}

	deleteQuery := fmt.Sprintf("DELETE FROM %s WHERE title = $1", table)
	res, err := c.Exec(deleteQuery, name)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (c *TJDBClient) ListTracking(category string) (artists []TrackingItem, songs []TrackingItem, err error) {
	if c == nil {
		return nil, nil, fmt.Errorf("TJ database client is nil")
	}

	if category == "all" || category == "artist" {
		rows, err := c.Query("SELECT title, COALESCE(start_from::text, '') FROM tracking_artists ORDER BY start_from DESC, title ASC")
		if err != nil {
			return nil, nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var item TrackingItem
			if err := rows.Scan(&item.Title, &item.StartFrom); err == nil {
				artists = append(artists, item)
			}
		}
	}

	if category == "all" || category == "song" {
		rows, err := c.Query("SELECT title, COALESCE(start_from::text, '') FROM tracking_songs ORDER BY start_from DESC, title ASC")
		if err != nil {
			return nil, nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var item TrackingItem
			if err := rows.Scan(&item.Title, &item.StartFrom); err == nil {
				songs = append(songs, item)
			}
		}
	}

	return artists, songs, nil
}

// InsertMatchedSong inserts a new song into matched_history only if pro does not exist (prevents duplicates).
func (c *TJDBClient) InsertMatchedSong(pro int, title string, artist string, publishDate string) (bool, error) {
	if c == nil {
		return false, fmt.Errorf("TJ database client is nil")
	}

	query := `
		INSERT INTO matched_history (pro, title, artist, publish_date, matched_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (pro) DO NOTHING
	`
	res, err := c.Exec(query, pro, title, artist, publishDate)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

func handleTJCommand(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	if tjDB == nil {
		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "⚠️ TJ 데이터베이스에 연결할 수 없습니다.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	options := ic.ApplicationCommandData().Options
	if len(options) == 0 {
		return
	}

	subCmd := options[0]
	switch subCmd.Name {
	case "add":
		handleTJAdd(s, ic, subCmd.Options)
	case "delete":
		handleTJDelete(s, ic, subCmd.Options)
	case "list":
		handleTJList(s, ic, subCmd.Options)
	}
}

func handleTJAdd(s *discordgo.Session, ic *discordgo.InteractionCreate, options []*discordgo.ApplicationCommandInteractionDataOption) {
	var category, name string
	for _, opt := range options {
		if opt.Name == "category" {
			category = opt.StringValue()
		} else if opt.Name == "name" {
			name = opt.StringValue()
		}
	}

	if category == "" || name == "" {
		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "⚠️ 구분(category)과 이름(name)을 입력해 주세요.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	added, err := tjDB.AddTracking(category, name)
	if err != nil {
		slog.Error("Failed to add TJ tracking", "category", category, "name", name, "error", err)
		errMsg := fmt.Sprintf("❌ 트래킹 등록 중 오류가 발생했습니다: %v", err)
		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: errMsg,
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	var categoryLabel string
	if category == "artist" {
		categoryLabel = "아티스트"
	} else {
		categoryLabel = "곡 제목"
	}

	if !added {
		msg := fmt.Sprintf("⚠️ `%s` 은(는) 이미 [%s] 트래킹 목록에 등록되어 있습니다.", name, categoryLabel)
		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: msg,
			},
		})
		return
	}

	todayStr := time.Now().Format("2006-01-02")
	msg := fmt.Sprintf("✅ [%s] **\"%s\"** 이(가) TJ 트래킹 목록에 추가되었습니다. (등록일: %s)", categoryLabel, name, todayStr)
	_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: msg,
		},
	})
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
		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "⚠️ 구분(category)과 이름(name)을 입력해 주세요.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	rowsAffected, err := tjDB.DeleteTracking(category, name)
	if err != nil {
		slog.Error("Failed to delete TJ tracking", "category", category, "name", name, "error", err)
		errMsg := fmt.Sprintf("❌ 트래킹 삭제 중 오류가 발생했습니다: %v", err)
		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: errMsg,
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	var categoryLabel string
	if category == "artist" {
		categoryLabel = "아티스트"
	} else {
		categoryLabel = "곡 제목"
	}

	if rowsAffected == 0 {
		msg := fmt.Sprintf("⚠️ `%s` 은(는) [%s] 트래킹 목록에 존재하지 않습니다.", name, categoryLabel)
		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: msg,
			},
		})
		return
	}

	msg := fmt.Sprintf("🗑️ [%s] **\"%s\"** 이(가) TJ 트래킹 목록에서 삭제되었습니다.", categoryLabel, name)
	_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: msg,
		},
	})
}

func handleTJList(s *discordgo.Session, ic *discordgo.InteractionCreate, options []*discordgo.ApplicationCommandInteractionDataOption) {
	category := "all"
	for _, opt := range options {
		if opt.Name == "category" {
			category = opt.StringValue()
		}
	}

	artists, songs, err := tjDB.ListTracking(category)
	if err != nil {
		slog.Error("Failed to list TJ tracking", "error", err)
		errMsg := fmt.Sprintf("❌ 트래킹 목록 조회 중 오류가 발생했습니다: %v", err)
		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: errMsg,
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	embed := &discordgo.MessageEmbed{
		Title: "🎵 TJ 노래방 트래킹 목록",
		Color: 0x3498db,
	}

	if category == "all" || category == "artist" {
		var artistStr string
		if len(artists) == 0 {
			artistStr = "(등록된 트래킹 아티스트가 없습니다)"
		} else {
			var lines []string
			for _, a := range artists {
				line := fmt.Sprintf("• **%s** `(%s)`", a.Title, a.StartFrom)
				lines = append(lines, line)
			}
			artistStr = strings.Join(lines, "\n")
		}

		// Discord embed field limit safety (1000 runes)
		artistRunes := []rune(artistStr)
		if len(artistRunes) > 1000 {
			artistStr = string(artistRunes[:990]) + "\n... (외 다수)"
		}

		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   fmt.Sprintf("🎤 트래킹 아티스트 (%d명)", len(artists)),
			Value:  artistStr,
			Inline: false,
		})
	}

	if category == "all" || category == "song" {
		var songStr string
		if len(songs) == 0 {
			songStr = "(등록된 트래킹 곡이 없습니다)"
		} else {
			var lines []string
			for _, sg := range songs {
				line := fmt.Sprintf("• **%s** `(%s)`", sg.Title, sg.StartFrom)
				lines = append(lines, line)
			}
			songStr = strings.Join(lines, "\n")
		}

		// Discord embed field limit safety (1000 runes)
		songRunes := []rune(songStr)
		if len(songRunes) > 1000 {
			songStr = string(songRunes[:990]) + "\n... (외 다수)"
		}

		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   fmt.Sprintf("🎶 트래킹 곡 (%d곡)", len(songs)),
			Value:  songStr,
			Inline: false,
		})
	}

	_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
		},
	})
}
