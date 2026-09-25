package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisClient struct {
	rdb *redis.Client
}

var redisClient *RedisClient

func InitRedis(redisURL string) error {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		// Fallback to simple Addr if not a URL format
		opts = &redis.Options{
			Addr: redisURL,
		}
	}

	rdb := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		slog.Error("Failed to connect to Redis", "url", redisURL, "error", err)
		return err
	}

	redisClient = &RedisClient{rdb: rdb}
	slog.Info("Successfully connected to Redis", "addr", opts.Addr)
	return nil
}

// SetSessionMessageID maps sessionID (key) to messageID (value) in Redis.
func (r *RedisClient) SetSessionMessageID(sessionID, messageID string) error {
	if r == nil || r.rdb == nil {
		return fmt.Errorf("redis client is nil")
	}
	ctx := context.Background()

	// Store key: ag:sess:<sessionID> -> messageID
	key := fmt.Sprintf("ag:sess:%s", sessionID)
	err := r.rdb.Set(ctx, key, messageID, 7*24*time.Hour).Err()
	if err != nil {
		return err
	}

	// Add session to set of active antigravity sessions
	_ = r.rdb.SAdd(ctx, "ag:sessions", sessionID).Err()
	return nil
}

// GetSessionMessageID gets messageID for a sessionID from Redis.
func (r *RedisClient) GetSessionMessageID(sessionID string) (string, error) {
	if r == nil || r.rdb == nil {
		return "", fmt.Errorf("redis client is nil")
	}
	ctx := context.Background()
	key := fmt.Sprintf("ag:sess:%s", sessionID)
	val, err := r.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil
	}
	return val, err
}

// GetAntigravitySessions returns all stored antigravity session IDs.
func (r *RedisClient) GetAntigravitySessions() ([]string, error) {
	if r == nil || r.rdb == nil {
		return nil, fmt.Errorf("redis client is nil")
	}
	ctx := context.Background()
	sessions, err := r.rdb.SMembers(ctx, "ag:sessions").Result()
	if err != nil {
		return nil, err
	}

	// Filter out empty strings if any
	var result []string
	for _, s := range sessions {
		s = strings.TrimSpace(s)
		if s != "" {
			result = append(result, s)
		}
	}
	return result, nil
}

type StoredSessionMeta struct {
	SessionID        string `json:"sessionId"`
	ProjectID        string `json:"projectId"`
	UserID           string `json:"userId"`
	AppID            string `json:"appId"`
	InteractionToken string `json:"interactionToken"`
	Prompt           string `json:"prompt"`
	Title            string `json:"title"`
	ChannelID        string `json:"channelId"`
	MessageID        string `json:"messageId"`
	Status           string `json:"status"`
	FinalResponse    string `json:"finalResponse"`
}

func extractShortTitle(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	// Replace line breaks with space
	prompt = strings.ReplaceAll(prompt, "\r\n", " ")
	prompt = strings.ReplaceAll(prompt, "\n", " ")
	prompt = strings.ReplaceAll(prompt, "\t", " ")
	for strings.Contains(prompt, "  ") {
		prompt = strings.ReplaceAll(prompt, "  ", " ")
	}
	runes := []rune(prompt)
	if len(runes) > 40 {
		return string(runes[:37]) + "..."
	}
	return prompt
}

func (r *RedisClient) SaveAntigravitySessionState(sess *AntigravitySessionState) error {
	if r == nil || r.rdb == nil || sess == nil {
		return nil
	}
	ctx := context.Background()
	sess.mu.Lock()
	title := sess.Title
	if title == "" && sess.Prompt != "" {
		title = extractShortTitle(sess.Prompt)
		sess.Title = title
	}
	meta := StoredSessionMeta{
		SessionID:        sess.SessionID,
		ProjectID:        sess.ProjectID,
		UserID:           sess.UserID,
		AppID:            sess.AppID,
		InteractionToken: sess.InteractionToken,
		Prompt:           sess.Prompt,
		Title:            title,
		ChannelID:        sess.ChannelID,
		MessageID:        sess.MessageID,
		Status:           sess.Status,
		FinalResponse:    sess.FinalResponse,
	}
	sess.mu.Unlock()

	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}

	key := fmt.Sprintf("ag:meta:%s", meta.SessionID)
	_ = r.rdb.Set(ctx, key, string(data), 30*24*time.Hour).Err()
	_ = r.rdb.SAdd(ctx, "ag:sessions", meta.SessionID).Err()
	return nil
}

func (r *RedisClient) LoadAllAntigravitySessionsFromRedis() []*AntigravitySessionState {
	if r == nil || r.rdb == nil {
		return nil
	}
	ctx := context.Background()
	sessionIDs, err := r.GetAntigravitySessions()
	if err != nil {
		return nil
	}

	var list []*AntigravitySessionState
	for _, id := range sessionIDs {
		key := fmt.Sprintf("ag:meta:%s", id)
		val, err := r.rdb.Get(ctx, key).Result()
		if err == nil && val != "" {
			var meta StoredSessionMeta
			if err := json.Unmarshal([]byte(val), &meta); err == nil {
				sess := &AntigravitySessionState{
					SessionID:        meta.SessionID,
					ProjectID:        meta.ProjectID,
					UserID:           meta.UserID,
					AppID:            meta.AppID,
					InteractionToken: meta.InteractionToken,
					Prompt:           meta.Prompt,
					Title:            meta.Title,
					ChannelID:        meta.ChannelID,
					MessageID:        meta.MessageID,
					Status:           meta.Status,
					FinalResponse:    meta.FinalResponse,
				}
				list = append(list, sess)
			}
		} else {
			list = append(list, &AntigravitySessionState{
				SessionID: id,
				Status:    "completed",
			})
		}
	}
	return list
}

func (r *RedisClient) SetLastAntigravitySessionID(sessionID string) error {
	if r == nil || r.rdb == nil {
		return nil
	}
	ctx := context.Background()
	return r.rdb.Set(ctx, "ag:last_session", sessionID, 30*24*time.Hour).Err()
}

func (r *RedisClient) GetLastAntigravitySessionID() string {
	if r == nil || r.rdb == nil {
		return ""
	}
	ctx := context.Background()
	val, err := r.rdb.Get(ctx, "ag:last_session").Result()
	if err != nil {
		return ""
	}
	return val
}

func (r *RedisClient) GetStoredSessionMeta(sessionID string) *StoredSessionMeta {
	if r == nil || r.rdb == nil || sessionID == "" {
		return nil
	}
	ctx := context.Background()
	key := fmt.Sprintf("ag:meta:%s", sessionID)
	val, err := r.rdb.Get(ctx, key).Result()
	if err != nil || val == "" {
		return nil
	}
	var meta StoredSessionMeta
	if err := json.Unmarshal([]byte(val), &meta); err == nil {
		return &meta
	}
	return nil
}

func (r *RedisClient) SetLastAntigravityProjectID(projectID string) error {
	if r == nil || r.rdb == nil || projectID == "" {
		return nil
	}
	ctx := context.Background()
	return r.rdb.Set(ctx, "ag:last_project", projectID, 30*24*time.Hour).Err()
}

func (r *RedisClient) GetLastAntigravityProjectID() string {
	if r == nil || r.rdb == nil {
		return ""
	}
	ctx := context.Background()
	val, err := r.rdb.Get(ctx, "ag:last_project").Result()
	if err != nil {
		return ""
	}
	return val
}

func (r *RedisClient) SetCodexPreferences(userID string, prefs CodexPreferences) error {
	if r == nil || r.rdb == nil {
		return nil
	}
	data, err := json.Marshal(prefs)
	if err != nil {
		return err
	}
	return r.rdb.Set(context.Background(), "codex:prefs:"+userID, data, 0).Err()
}

func (r *RedisClient) GetCodexPreferences(userID string) (CodexPreferences, bool) {
	if r == nil || r.rdb == nil {
		return CodexPreferences{}, false
	}
	data, err := r.rdb.Get(context.Background(), "codex:prefs:"+userID).Bytes()
	if err != nil {
		return CodexPreferences{}, false
	}
	var prefs CodexPreferences
	if json.Unmarshal(data, &prefs) != nil || prefs.ProjectID == "" {
		return CodexPreferences{}, false
	}
	return prefs, true
}

func (r *RedisClient) ClearAntigravityProjectSession(projectID string) error {
	if r == nil || r.rdb == nil || projectID == "" {
		return nil
	}
	ctx := context.Background()
	sessID := fmt.Sprintf("ag_proj_%s", projectID)
	_ = r.rdb.Del(ctx, fmt.Sprintf("ag:meta:%s", sessID)).Err()
	_ = r.rdb.Del(ctx, fmt.Sprintf("ag:state:%s", sessID)).Err()
	_ = r.rdb.SRem(ctx, "ag:sessions", sessID).Err()
	return nil
}

type WebLoginToken struct {
	Code       string    `json:"code"`
	UserID     string    `json:"userId"`
	Username   string    `json:"username"`
	GlobalName string    `json:"globalName"`
	AvatarURL  string    `json:"avatarUrl"`
	CreatedAt  time.Time `json:"createdAt"`
}

func (r *RedisClient) SaveWebLoginToken(token *WebLoginToken, ttl time.Duration) error {
	if r == nil || r.rdb == nil {
		return fmt.Errorf("redis client is nil")
	}
	ctx := context.Background()
	data, err := json.Marshal(token)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("ag:web_auth:%s", token.Code)
	return r.rdb.Set(ctx, key, string(data), ttl).Err()
}

type ChannelSpotifyState struct {
	ChannelID      string `json:"channel_id"`
	GuildID        string `json:"guild_id"`
	MessageID      string `json:"message_id"`
	PlaybackKey    string `json:"playback_key"`
	TargetUserID   string `json:"target_user_id"`
	TargetUsername string `json:"target_username"`
	OwnerID        string `json:"owner_id"`
	IsAuto         bool   `json:"is_auto"`
	IsDedicated    bool   `json:"is_dedicated"`
	ImageMode      bool   `json:"image_mode"`
	UpdatedAt      int64  `json:"updated_at"`
}

func (r *RedisClient) SaveChannelSpotifyState(state *ChannelSpotifyState) error {
	if r == nil || r.rdb == nil || state == nil || state.ChannelID == "" {
		return nil
	}
	ctx := context.Background()
	state.UpdatedAt = time.Now().Unix()
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("spotify:channel:%s", state.ChannelID)
	_ = r.rdb.Set(ctx, key, string(data), 14*24*time.Hour).Err()
	_ = r.rdb.SAdd(ctx, "spotify:channels", state.ChannelID).Err()
	return nil
}

func (r *RedisClient) GetChannelSpotifyState(channelID string) (*ChannelSpotifyState, error) {
	if r == nil || r.rdb == nil || channelID == "" {
		return nil, fmt.Errorf("redis client is nil or channelID empty")
	}
	ctx := context.Background()
	key := fmt.Sprintf("spotify:channel:%s", channelID)
	val, err := r.rdb.Get(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	var state ChannelSpotifyState
	if err := json.Unmarshal([]byte(val), &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func (r *RedisClient) DeleteChannelSpotifyState(channelID string) error {
	if r == nil || r.rdb == nil || channelID == "" {
		return nil
	}
	ctx := context.Background()
	key := fmt.Sprintf("spotify:channel:%s", channelID)
	_ = r.rdb.Del(ctx, key).Err()
	_ = r.rdb.SRem(ctx, "spotify:channels", channelID).Err()
	return nil
}

func (r *RedisClient) LoadAllChannelSpotifyStates() []*ChannelSpotifyState {
	if r == nil || r.rdb == nil {
		return nil
	}
	ctx := context.Background()
	channels, err := r.rdb.SMembers(ctx, "spotify:channels").Result()
	if err != nil {
		return nil
	}
	var states []*ChannelSpotifyState
	for _, chID := range channels {
		st, err := r.GetChannelSpotifyState(chID)
		if err == nil && st != nil {
			states = append(states, st)
		}
	}
	return states
}
