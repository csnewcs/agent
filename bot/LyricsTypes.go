package main

import (
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	LyricsPastelPink     = 0xFFB6C1
	LyricsContextLines   = 2
	LyricsMaxDescLen     = 3900
	LyricsUpdateInterval = 1 * time.Second
)

type LyricLine struct {
	TimeMs int    `json:"timeMs"`
	Text   string `json:"text"`
}

type LyricResult struct {
	Source             string      `json:"source"` // "lrclib" | "spotify" | "petitlyrics" | "youtube"
	TrackName          string      `json:"trackName"`
	Artist             string      `json:"artist"`
	Album              string      `json:"album"`
	DurationMs         int         `json:"durationMs"`
	Lines              []LyricLine `json:"lines"`
	TranslatedLines    []string    `json:"translatedLines,omitempty"`
	PronunciationLines []string    `json:"pronunciationLines,omitempty"`
	TranslationModel   string      `json:"translationModel,omitempty"`
	DetectedLang       string      `json:"detectedLang,omitempty"`
	CoverURL           string      `json:"coverUrl,omitempty"`
	SpotifyURL         string      `json:"spotifyUrl,omitempty"`
	mu                 sync.RWMutex
}

type RenderLyricsInput struct {
	CoverURL   string            `json:"cover_url"`
	DurationMs int               `json:"duration_ms"`
	OutDir     string            `json:"out_dir"`
	Lines      []RenderLyricLine `json:"lines"`
}

type RenderLyricLine struct {
	Text     string `json:"text"`
	Trans    string `json:"trans"`
	Phonetic string `json:"phonetic"`
	TimeMs   int    `json:"time_ms"`
}

type QueuedLyricsItem struct {
	Query     string
	Result    *LyricResult
	Err       error
	ReadyChan chan struct{}
	FramesDir string
}

type LyricsPlaybackSession struct {
	Key         string
	OwnerID     string
	ChannelID   string
	MessageID   string
	SearchQuery string
	TrackTitle  string
	ArtistName  string
	CoverURL    string
	Result      *LyricResult
	StartTime   time.Time
	Offset      time.Duration
	ImageMode   bool
	FramesDir   string
	Queue       []*QueuedLyricsItem
	mu          sync.RWMutex
	editMu      sync.Mutex
	LastIdx     int
	Stopped     atomic.Bool
	Done        chan struct{}
	IsAuto      bool
	IsDedicated bool
	Interaction *discordgo.Interaction
}

func (s *LyricsPlaybackSession) Enqueue(query string) (*QueuedLyricsItem, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := &QueuedLyricsItem{
		Query:     query,
		ReadyChan: make(chan struct{}),
	}
	s.Queue = append(s.Queue, item)
	return item, len(s.Queue)
}

func (s *LyricsPlaybackSession) Dequeue() (*QueuedLyricsItem, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Queue) == 0 {
		return nil, false
	}
	next := s.Queue[0]
	s.Queue = s.Queue[1:]
	return next, true
}

func (s *LyricsPlaybackSession) QueueLength() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.Queue)
}

func (s *LyricsPlaybackSession) ClearQueue() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.Queue {
		if item != nil && item.FramesDir != "" {
			_ = os.RemoveAll(item.FramesDir)
		}
	}
	s.Queue = nil
}

type SpotifyActivityRecord struct {
	Activity  *discordgo.Activity
	UpdatedAt time.Time
	GuildID   string
}

var (
	activeLyricsPlaybacks   sync.Map // map[playbackKey string]*LyricsPlaybackSession
	channelLyricsPlaybacks  sync.Map // map[channelID string]*LyricsPlaybackSession
	latestSpotifyActivities sync.Map // map[userID string]*SpotifyActivityRecord

	lyricsGlobalCacheMu sync.RWMutex
	lyricsGlobalCache   = make(map[string]*LyricResult)
)

func getCachedLyrics(key string) *LyricResult {
	if key == "" {
		return nil
	}
	lyricsGlobalCacheMu.RLock()
	defer lyricsGlobalCacheMu.RUnlock()
	return lyricsGlobalCache[key]
}

func setCachedLyrics(key string, res *LyricResult) {
	if key == "" || res == nil {
		return
	}
	lyricsGlobalCacheMu.Lock()
	defer lyricsGlobalCacheMu.Unlock()
	if len(lyricsGlobalCache) > 100 {
		for k := range lyricsGlobalCache {
			delete(lyricsGlobalCache, k)
			break
		}
	}
	lyricsGlobalCache[key] = res
}

func StopAndClearChannelPlayback(channelID string) {
	if channelID == "" {
		return
	}
	if val, ok := channelLyricsPlaybacks.Load(channelID); ok && val != nil {
		old := val.(*LyricsPlaybackSession)
		old.Stopped.Store(true)
		select {
		case <-old.Done:
		default:
			close(old.Done)
		}
		channelLyricsPlaybacks.Delete(channelID)
		activeLyricsPlaybacks.Delete(old.Key)
	}
}
