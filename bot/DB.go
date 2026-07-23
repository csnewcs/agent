package main

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/bwmarrin/discordgo"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type DBClient struct {
	*sql.DB
}

func NewDBClient(cfg *Config) (*DBClient, error) {
	db, err := sql.Open("pgx", cfg.DBURL)
	if err != nil {
		return nil, err
	}

	if err := db.Ping(); err != nil {
		return nil, err
	}

	client := &DBClient{db}
	if err := client.ensureTables(); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

func (c *DBClient) ensureTables() error {
	_, err := c.Exec(`
	CREATE TABLE IF NOT EXISTS n8n_chat_histories (
		id SERIAL PRIMARY KEY,
		session_id VARCHAR(255) NOT NULL,
		message JSONB NOT NULL
	)`)
	if err != nil {
		return err
	}

	_, err = c.Exec(`
	CREATE TABLE IF NOT EXISTS n8n_chat_sessions (
		session_id VARCHAR(255) PRIMARY KEY,
		title VARCHAR(255),
		is_active BOOLEAN NOT NULL DEFAULT FALSE
	)`)
	return err
}

func SaveMessage(db *DBClient, message *discordgo.MessageCreate) error {
	if db == nil || message == nil || message.Author == nil {
		return fmt.Errorf("invalid message or database client")
	}

	hasAttachment := len(message.Attachments) > 0
	attachmentURL := "undefined"
	if hasAttachment {
		attachmentURL = message.Attachments[0].URL
	}

	contentStr := fmt.Sprintf("u: %s a: %t l: %s c: %s", message.Author.ID, hasAttachment, attachmentURL, message.Content)

	responseMetadata := make(map[string]interface{})
	if message.ReferencedMessage != nil {
		responseMetadata["reply_to"] = message.ReferencedMessage.Content
	}

	chatMsg := N8NMessage{
		Type:             "human",
		Content:          contentStr,
		AdditionalKwargs: make(map[string]interface{}),
		ResponseMetadata: responseMetadata,
	}

	msgJSON, err := json.Marshal(chatMsg)
	if err != nil {
		return err
	}

	_, err = db.Exec(`
	INSERT INTO n8n_chat_histories (session_id, message)
	VALUES ($1, $2)`,
		message.ChannelID,
		msgJSON,
	)
	if err != nil {
		return err
	}

	return pruneMessages(db, message.ChannelID, 30)
}

func pruneMessages(db *DBClient, sessionID string, limit int) error {
	if db == nil {
		return fmt.Errorf("database client is nil")
	}

	_, err := db.Exec(`
	DELETE FROM n8n_chat_histories
	WHERE id NOT IN (
		SELECT id FROM n8n_chat_histories
		WHERE session_id = $1
		ORDER BY id DESC
		LIMIT $2
	) AND session_id = $1`, sessionID, limit)
	return err
}

func GetRecentMessages(db *DBClient, sessionID string, limit int) ([]N8NMessage, error) {
	if db == nil {
		return nil, fmt.Errorf("database client is nil")
	}

	rows, err := db.Query(`
	SELECT message
	FROM n8n_chat_histories
	WHERE session_id = $1
	ORDER BY id DESC
	LIMIT $2`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	messages := make([]N8NMessage, 0, limit)
	for rows.Next() {
		var msgJSON []byte
		if err := rows.Scan(&msgJSON); err != nil {
			return nil, err
		}
		var msg N8NMessage
		if err := json.Unmarshal(msgJSON, &msg); err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Reverse the messages to chronological order
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	return messages, nil
}

type N8NMessage struct {
	Type             string                 `json:"type"`
	Content          string                 `json:"content"`
	AdditionalKwargs map[string]interface{} `json:"additional_kwargs"`
	ResponseMetadata map[string]interface{} `json:"response_metadata"`
}

func GetMaxSessionID(db *DBClient) (int, error) {
	if db == nil {
		return 1, fmt.Errorf("database client is nil")
	}

	var maxSessionID sql.NullInt64
	err := db.QueryRow(`
		SELECT MAX(CAST(session_id AS INTEGER))
		FROM n8n_chat_histories
		WHERE session_id ~ '^[0-9]+$' AND LENGTH(session_id) < 10
	`).Scan(&maxSessionID)

	if err != nil {
		return 1, err
	}

	if maxSessionID.Valid {
		return int(maxSessionID.Int64), nil
	}

	return 1, nil
}

func ActivateSession(db *DBClient, sessionID string) error {
	if db == nil {
		return fmt.Errorf("database client is nil")
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Deactivate all
	_, err = tx.Exec(`UPDATE n8n_chat_sessions SET is_active = FALSE`)
	if err != nil {
		return err
	}

	// Insert or Update to activate the specific session
	_, err = tx.Exec(`
		INSERT INTO n8n_chat_sessions (session_id, is_active)
		VALUES ($1, TRUE)
		ON CONFLICT (session_id) DO UPDATE SET is_active = TRUE
	`, sessionID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func GetActiveSessionID(db *DBClient) (int, error) {
	if db == nil {
		return 1, fmt.Errorf("database client is nil")
	}

	var sessionIDStr string
	err := db.QueryRow(`
		SELECT session_id
		FROM n8n_chat_sessions
		WHERE is_active = TRUE
		LIMIT 1
	`).Scan(&sessionIDStr)

	if err == nil {
		// Found active session!
		var val int
		if _, err := fmt.Sscan(sessionIDStr, &val); err == nil {
			return val, nil
		}
	}

	// Fallback to max session id from chat histories
	return GetMaxSessionID(db)
}

func IsSessionTitleEmpty(db *DBClient, sessionID string) (bool, error) {
	if db == nil {
		return false, fmt.Errorf("database client is nil")
	}

	var title sql.NullString
	err := db.QueryRow(`
		SELECT title
		FROM n8n_chat_sessions
		WHERE session_id = $1
	`, sessionID).Scan(&title)

	if err == sql.ErrNoRows {
		// Session does not exist yet (title is empty)
		return true, nil
	} else if err != nil {
		return false, err
	}

	return !title.Valid || title.String == "", nil
}

func UpdateSessionTitle(db *DBClient, sessionID string, title string) error {
	if db == nil {
		return fmt.Errorf("database client is nil")
	}

	_, err := db.Exec(`
		INSERT INTO n8n_chat_sessions (session_id, title)
		VALUES ($1, $2)
		ON CONFLICT (session_id) DO UPDATE SET title = EXCLUDED.title
	`, sessionID, title)
	return err
}

type SessionInfo struct {
	SessionID string
	Title     string
	IsActive  bool
}

func GetSessions(db *DBClient) ([]SessionInfo, error) {
	if db == nil {
		return nil, fmt.Errorf("database client is nil")
	}

	rows, err := db.Query(`
		SELECT session_id, COALESCE(title, ''), is_active
		FROM n8n_chat_sessions
		ORDER BY CAST(session_id AS INTEGER) DESC
		LIMIT 25
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []SessionInfo
	for rows.Next() {
		var s SessionInfo
		if err := rows.Scan(&s.SessionID, &s.Title, &s.IsActive); err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	return sessions, nil
}

func DeleteSession(db *DBClient, sessionID string) (bool, error) {
	if db == nil {
		return false, fmt.Errorf("database client is nil")
	}

	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	// Check if this session was active before deleting
	var isActive bool
	err = tx.QueryRow(`
		SELECT is_active
		FROM n8n_chat_sessions
		WHERE session_id = $1
	`, sessionID).Scan(&isActive)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}

	// Delete from sessions table
	_, err = tx.Exec(`DELETE FROM n8n_chat_sessions WHERE session_id = $1`, sessionID)
	if err != nil {
		return false, err
	}

	// Delete from histories table
	_, err = tx.Exec(`DELETE FROM n8n_chat_histories WHERE session_id = $1`, sessionID)
	if err != nil {
		return false, err
	}

	err = tx.Commit()
	if err != nil {
		return false, err
	}

	return isActive, nil
}
