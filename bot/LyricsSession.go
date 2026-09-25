package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

func (s *LyricsPlaybackSession) PreRenderLyricsFramesSync() {
	if !s.ImageMode || s.Result == nil || len(s.Result.Lines) == 0 {
		return
	}
	if s.FramesDir == "" {
		s.FramesDir = fmt.Sprintf("/tmp/lyrics_frames_%s", s.Key)
	}
	_ = os.MkdirAll(s.FramesDir, 0755)

	s.Result.mu.RLock()
	var inputLines []RenderLyricLine
	for idx, line := range s.Result.Lines {
		var trans, pron string
		if idx < len(s.Result.TranslatedLines) {
			trans = s.Result.TranslatedLines[idx]
		}
		if idx < len(s.Result.PronunciationLines) {
			pron = s.Result.PronunciationLines[idx]
		}
		inputLines = append(inputLines, RenderLyricLine{
			Text:     line.Text,
			Trans:    trans,
			Phonetic: pron,
			TimeMs:   line.TimeMs,
		})
	}
	coverURL := s.Result.CoverURL
	durMs := s.Result.DurationMs
	s.Result.mu.RUnlock()

	if err := RenderLyricsFramesNative(coverURL, durMs, inputLines, s.FramesDir); err != nil {
		slog.Warn("Failed to render lyrics frames natively in Go", "error", err)
	}
}

func (s *LyricsPlaybackSession) PreRenderLyricsFrames() {
	go s.PreRenderLyricsFramesSync()
}

func (s *LyricsPlaybackSession) TriggerImmediateUpdate(sess *discordgo.Session, ic *discordgo.InteractionCreate) {
	s.mu.Lock()
	if s.Stopped.Load() || s.Result == nil {
		s.mu.Unlock()
		return
	}
	res := s.Result
	elapsed := int((time.Since(s.StartTime) + s.Offset).Milliseconds())
	if elapsed < 0 {
		elapsed = 0
	}
	currentIdx := findCurrentLyricsIndex(res.Lines, elapsed)
	s.LastIdx = currentIdx
	key := s.Key
	s.mu.Unlock()

	hasLyrics := len(res.Lines) > 0
	buttons := buildLyricsButtons(key, hasLyrics, false, false, s.IsAuto, s.IsDedicated, s.QueueLength())
	desc := buildLyricsDescription(res, currentIdx)
	footer := buildLyricsFooter(res, elapsed)

	_, err := editLyricsMessage(sess, ic, s, res.TrackName, res.Artist, desc, footer, buttons)
	if err != nil {
		slog.Warn("Failed to trigger immediate lyrics update", "error", err)
	} else {
		slog.Info("Successfully triggered immediate lyrics update with new translation", "track", res.TrackName, "idx", currentIdx)
	}
}

func (s *LyricsPlaybackSession) GetStartTime() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.StartTime
}

func (s *LyricsPlaybackSession) GetOffset() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Offset
}

func (s *LyricsPlaybackSession) AdjustOffset(delta time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Offset += delta
}

func (s *LyricsPlaybackSession) ResetOffset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Offset = 0
}

func (s *LyricsPlaybackSession) GetElapsedMs() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	elapsed := int((time.Since(s.StartTime) + s.Offset).Milliseconds())
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

func (s *LyricsPlaybackSession) AdjustStartTime(delta time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.StartTime = s.StartTime.Add(delta)
}

func (s *LyricsPlaybackSession) GetInteraction() *discordgo.Interaction {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Interaction
}

func (s *LyricsPlaybackSession) SetInteraction(inter *discordgo.Interaction) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Interaction = inter
}

func findSpotifyActivity(activities []*discordgo.Activity) *discordgo.Activity {
	for _, act := range activities {
		if act != nil && (strings.EqualFold(act.Name, "Spotify") || act.Type == discordgo.ActivityTypeListening || strings.HasPrefix(act.Party.ID, "spotify:")) {
			return act
		}
	}
	return nil
}

func cacheSpotifyPresenceUpdate(p *discordgo.PresenceUpdate) {
	if p == nil || p.User == nil || p.User.ID == "" {
		return
	}
	// A presence update without Spotify is authoritative too: keep a tombstone
	// so an older guild-state snapshot cannot resurrect the previous song.
	latestSpotifyActivities.Store(p.User.ID, &SpotifyActivityRecord{
		Activity:  findSpotifyActivity(p.Activities),
		UpdatedAt: time.Now(),
		GuildID:   p.GuildID,
	})
}

func currentSpotifyActivity(rec *SpotifyActivityRecord, now time.Time) *discordgo.Activity {
	if rec == nil || rec.Activity == nil {
		return nil
	}
	if end := rec.Activity.Timestamps.EndTimestamp; end > 0 {
		if now.After(time.UnixMilli(end).Add(30 * time.Second)) {
			return nil
		}
	} else if now.Sub(rec.UpdatedAt) > 30*time.Minute {
		// Without a track end timestamp, do not trust an unchanged presence forever.
		return nil
	}
	return rec.Activity
}

func getSpotifyActivity(s *discordgo.Session, targetUserID, guildID string) *discordgo.Activity {
	if targetUserID == "" {
		return nil
	}
	now := time.Now()
	if val, ok := latestSpotifyActivities.Load(targetUserID); ok {
		rec, _ := val.(*SpotifyActivityRecord)
		return currentSpotifyActivity(rec, now)
	}
	if s == nil || s.State == nil {
		return nil
	}

	// The state snapshot is only a startup fallback. Store it once; only a new
	// gateway PresenceUpdate may refresh the observation time afterward.
	cacheSnapshot := func(act *discordgo.Activity, observedGuildID string) *discordgo.Activity {
		rec := &SpotifyActivityRecord{Activity: act, UpdatedAt: now, GuildID: observedGuildID}
		actual, _ := latestSpotifyActivities.LoadOrStore(targetUserID, rec)
		stored, _ := actual.(*SpotifyActivityRecord)
		return currentSpotifyActivity(stored, now)
	}
	if guildID != "" {
		if p, err := s.State.Presence(guildID, targetUserID); err == nil && p != nil {
			if act := findSpotifyActivity(p.Activities); act != nil {
				return cacheSnapshot(act, guildID)
			}
			return nil
		}
	}
	for _, g := range s.State.Guilds {
		if p, err := s.State.Presence(g.ID, targetUserID); err == nil && p != nil {
			if act := findSpotifyActivity(p.Activities); act != nil {
				return cacheSnapshot(act, g.ID)
			}
			return nil
		}
	}
	return nil
}

func isTrackMatching(spTitle, spArtist, expTitle, expArtist string) bool {
	if spTitle == "" || expTitle == "" {
		return false
	}
	if strings.EqualFold(spTitle, expTitle) {
		return true
	}
	s1 := strings.ToLower(strings.TrimSpace(spTitle))
	s2 := strings.ToLower(strings.TrimSpace(expTitle))
	if s1 == s2 || strings.Contains(s1, s2) || strings.Contains(s2, s1) {
		return true
	}
	cleanS1 := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (r >= 0xAC00 && r <= 0xD7A3) || (r >= 0x3040 && r <= 0x30FF) || (r >= 0x4E00 && r <= 0x9FFF) {
			return r
		}
		return ' '
	}, s1)
	cleanS2 := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (r >= 0xAC00 && r <= 0xD7A3) || (r >= 0x3040 && r <= 0x30FF) || (r >= 0x4E00 && r <= 0x9FFF) {
			return r
		}
		return ' '
	}, s2)
	f1 := strings.Fields(cleanS1)
	f2 := strings.Fields(cleanS2)
	if len(f1) > 0 && len(f2) > 0 && f1[0] == f2[0] {
		return true
	}
	return false
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// CalibrateWithSpotifyActivity periodically calibrates pbSession.StartTime with live Spotify presence timestamps.
// Large drift (|diff| >= 1000ms) snaps and forces lyrics re-indexing.
// Moderate drift (|diff| >= 250ms) smoothly aligns StartTime.
func CalibrateWithSpotifyActivity(pbSession *LyricsPlaybackSession, act *discordgo.Activity, expectedTrack, expectedArtist string) bool {
	if act == nil || act.Timestamps.StartTimestamp <= 0 || pbSession == nil {
		return false
	}

	if expectedTrack != "" {
		spTitle := strings.TrimSpace(act.Details)
		spArtist := strings.TrimSpace(act.State)
		if !isTrackMatching(spTitle, spArtist, expectedTrack, expectedArtist) {
			return false
		}
	}

	spStartTime := time.UnixMilli(act.Timestamps.StartTimestamp)
	pbSession.mu.RLock()
	curStartTime := pbSession.StartTime
	pbSession.mu.RUnlock()

	diff := curStartTime.Sub(spStartTime)

	// Large drift or seek/pause-resume
	if absDuration(diff) >= 1000*time.Millisecond {
		pbSession.mu.Lock()
		pbSession.StartTime = spStartTime
		pbSession.LastIdx = -1
		pbSession.mu.Unlock()
		slog.Info("Calibrated Spotify playback position (seek/pause-resume)",
			"key", pbSession.Key,
			"drift_ms", diff.Milliseconds(),
			"new_start", spStartTime.Format("15:04:05.000"),
		)
		return true
	}

	// Moderate drift: periodic smooth alignment
	if absDuration(diff) >= 250*time.Millisecond {
		pbSession.mu.Lock()
		pbSession.StartTime = spStartTime
		pbSession.mu.Unlock()
		return false
	}

	return false
}

func runSpotifyAutoTrackingLoop(
	s *discordgo.Session,
	pbSession *LyricsPlaybackSession,
	targetUserID, targetUsername, guildID string,
) {
	playbackKey := pbSession.Key
	channelID := pbSession.ChannelID

	defer func() {
		activeLyricsPlaybacks.Delete(playbackKey)
		channelLyricsPlaybacks.Delete(channelID)
		if pbSession.FramesDir != "" {
			_ = os.RemoveAll(pbSession.FramesDir)
		}
	}()

	ticker := time.NewTicker(LyricsUpdateInterval)
	defer ticker.Stop()

	var currentTrackKey string
	var currentResult *LyricResult
	var busy bool
	var noMusicCount int

	for {
		select {
		case <-pbSession.Done:
			return
		case <-ticker.C:
			if pbSession.Stopped.Load() {
				return
			}

			// Ensure Redis has up-to-date messageID
			pbSession.mu.RLock()
			msgID := pbSession.MessageID
			isDedicated := pbSession.IsDedicated
			pbSession.mu.RUnlock()
			if msgID != "" && redisClient != nil {
				_ = redisClient.SaveChannelSpotifyState(&ChannelSpotifyState{
					ChannelID:      channelID,
					GuildID:        guildID,
					MessageID:      msgID,
					PlaybackKey:    playbackKey,
					TargetUserID:   targetUserID,
					TargetUsername: targetUsername,
					OwnerID:        pbSession.OwnerID,
					IsAuto:         true,
					IsDedicated:    isDedicated,
					ImageMode:      pbSession.ImageMode,
				})
			}

			act := getSpotifyActivity(s, targetUserID, guildID)
			if act == nil {
				noMusicCount++
				// If no music for 15 minutes (900s) on NON-dedicated sessions, auto close
				if !pbSession.IsDedicated && noMusicCount > 900 {
					pbSession.Stopped.Store(true)
					if redisClient != nil {
						_ = redisClient.DeleteChannelSpotifyState(channelID)
					}
					_ = deleteLyricsMessage(s, nil, pbSession)
					return
				}

				if currentResult != nil && noMusicCount == 15 {
					pbSession.mu.Lock()
					pbSession.Result = nil
					pbSession.LastIdx = -1
					pbSession.mu.Unlock()
					currentResult = nil
					currentTrackKey = ""

					buttons := buildLyricsButtons(playbackKey, false, false, false, true, pbSession.IsDedicated, 0)
					title := fmt.Sprintf("🎧 %s님의 Spotify 재생 일시정지됨", targetUsername)
					desc := "Spotify 음악이 멈췄습니다. 다시 음악을 재생하면 자동으로 계속 추적합니다...\n\n⏹️ **종료하려면 아래 중지 버튼을 눌러주세요.**"
					footer := "자동 추적 대기 중"
					if pbSession.IsDedicated {
						footer = "상시 고정 전용 채널 • 자동 추적 대기 중"
					}
					_, _ = editLyricsMessage(s, nil, pbSession, title, "", desc, footer, buttons)
				}
				continue
			}

			noMusicCount = 0
			trackTitle := strings.TrimSpace(act.Details)
			artistName := strings.TrimSpace(act.State)
			trackIdentity := fmt.Sprintf("%s|%s", strings.ToLower(trackTitle), strings.ToLower(artistName))

			pbSession.mu.RLock()
			sessRes := pbSession.Result
			pbSession.mu.RUnlock()
			if sessRes != nil && currentResult != sessRes {
				sessIdentity := fmt.Sprintf("%s|%s", strings.ToLower(sessRes.TrackName), strings.ToLower(sessRes.Artist))
				if sessIdentity == trackIdentity {
					currentResult = sessRes
					currentTrackKey = trackIdentity
				}
			}

			if currentResult != nil && trackIdentity == currentTrackKey {
				if act.Timestamps.StartTimestamp > 0 {
					CalibrateWithSpotifyActivity(pbSession, act, currentResult.TrackName, currentResult.Artist)
					if act.Timestamps.EndTimestamp > act.Timestamps.StartTimestamp {
						spDuration := int(act.Timestamps.EndTimestamp - act.Timestamps.StartTimestamp)
						if currentResult.DurationMs <= 0 || (spDuration > 0 && absInt(currentResult.DurationMs-spDuration) > 4000) {
							currentResult.DurationMs = spDuration
						}
					}
				}
			} else if trackIdentity != currentTrackKey && !busy {
				currentTrackKey = trackIdentity
				busy = true

				buttons := buildLyricsButtons(playbackKey, false, false, false, true, pbSession.IsDedicated, 0)
				desc := "⏳ **다음 곡 가사와 번역을 불러오는 중...**"
				_, _ = editLyricsMessage(s, nil, pbSession, trackTitle, artistName, desc, "가사 검색 중", buttons)

				searchQuery := trackTitle
				if artistName != "" {
					searchQuery = trackTitle + " " + artistName
				}
				res, err := fetchLyricsConcurrent(searchQuery, trackTitle, artistName, false)
				if err != nil {
					slog.Warn("Auto tracking: failed to fetch lyrics", "track", trackTitle, "artist", artistName, "error", err)
					notFoundDesc := "❌ **가사를 찾을 수 없습니다.**\n\n🔍 **유사 검색을 시도하려면 아래 버튼을 눌러주세요.**\n⏹️ **종료하려면 중지 버튼을 눌러주세요.**"
					buttons := buildLyricsButtons(playbackKey, false, false, true, true, pbSession.IsDedicated, 0)

					var spotCover string
					if strings.HasPrefix(act.Assets.LargeImageID, "spotify:") {
						spotCover = fmt.Sprintf("https://i.scdn.co/image/%s", strings.TrimPrefix(act.Assets.LargeImageID, "spotify:"))
					}

					pbSession.mu.Lock()
					pbSession.SearchQuery = searchQuery
					pbSession.TrackTitle = trackTitle
					pbSession.ArtistName = artistName
					pbSession.CoverURL = spotCover
					pbSession.Result = nil
					pbSession.LastIdx = -1
					pbSession.mu.Unlock()

					if pbSession.ImageMode {
						if pbSession.FramesDir == "" {
							pbSession.FramesDir = fmt.Sprintf("/tmp/lyrics_frames_%s", pbSession.Key)
						}
						_ = os.MkdirAll(pbSession.FramesDir, 0755)
						nfPath := fmt.Sprintf("%s/frame_000.jpg", pbSession.FramesDir)
						_ = RenderNotFoundLyricsFrame(spotCover, nfPath)
						pbSession.mu.Lock()
						pbSession.LastIdx = 0
						pbSession.mu.Unlock()
					}

					_, _ = editLyricsMessage(s, nil, pbSession, trackTitle, artistName, notFoundDesc, "자동 추적 대기 중", buttons)

					currentResult = nil
					busy = false
					continue
				}

				if strings.HasPrefix(act.Assets.LargeImageID, "spotify:") {
					res.CoverURL = fmt.Sprintf("https://i.scdn.co/image/%s", strings.TrimPrefix(act.Assets.LargeImageID, "spotify:"))
				}
				if act.Timestamps.EndTimestamp > act.Timestamps.StartTimestamp && act.Timestamps.StartTimestamp > 0 {
					res.DurationMs = int(act.Timestamps.EndTimestamp - act.Timestamps.StartTimestamp)
				}

				geminiKey := ""
				openAIKey := ""
				if botConfig != nil {
					geminiKey = botConfig.GeminiAPIKey
					openAIKey = botConfig.OpenAIAPIKey
				}

				var sTime time.Time
				if act.Timestamps.StartTimestamp > 0 {
					sTime = time.UnixMilli(act.Timestamps.StartTimestamp)
				} else {
					sTime = time.Now()
				}

				pbSession.mu.Lock()
				pbSession.Result = res
				pbSession.StartTime = sTime
				pbSession.Offset = 0
				pbSession.LastIdx = -1
				pbSession.mu.Unlock()

				if pbSession.ImageMode && len(res.Lines) > 0 {
					pbSession.PreRenderLyricsFramesSync()
				}

				currentResult = res
				busy = false

				targetRes := res
				if (geminiKey != "" || openAIKey != "") && len(targetRes.Lines) > 0 {
					go func() {
						attachLyricsTranslation(targetRes, geminiKey, openAIKey)
						if pbSession.ImageMode {
							pbSession.PreRenderLyricsFramesSync()
						}
						pbSession.mu.Lock()
						if pbSession.Result == targetRes {
							pbSession.LastIdx = -1
						}
						pbSession.mu.Unlock()
						pbSession.TriggerImmediateUpdate(s, nil)
					}()
				}
			}

			if currentResult != nil && !busy {
				elapsed := pbSession.GetElapsedMs()
				currentIdx := findCurrentLyricsIndex(currentResult.Lines, elapsed)

				if currentResult.DurationMs > 0 && elapsed >= currentResult.DurationMs+3000 {
					if pbSession.LastIdx != -999 {
						pbSession.mu.Lock()
						pbSession.LastIdx = -999
						pbSession.mu.Unlock()

						finishedSourceLabel := "LRCLIB"
						if currentResult.Source == "spotify" {
							finishedSourceLabel = "Spotify"
						} else if currentResult.Source == "petitlyrics" {
							finishedSourceLabel = "PetitLyrics"
						}
						currentResult.mu.RLock()
						fModel := currentResult.TranslationModel
						fLang := strings.ToUpper(strings.TrimSpace(currentResult.DetectedLang))
						currentResult.mu.RUnlock()
						var fFooter string
						if fModel != "" {
							if fLang != "" {
								fFooter = fmt.Sprintf("소스: %s • 번역: %s (%s) | %s / %s", finishedSourceLabel, fModel, fLang, fmtMs(currentResult.DurationMs), fmtMs(currentResult.DurationMs))
							} else {
								fFooter = fmt.Sprintf("소스: %s • 번역: %s | %s / %s", finishedSourceLabel, fModel, fmtMs(currentResult.DurationMs), fmtMs(currentResult.DurationMs))
							}
						} else {
							fFooter = fmt.Sprintf("소스: %s | %s / %s", finishedSourceLabel, fmtMs(currentResult.DurationMs), fmtMs(currentResult.DurationMs))
						}

						buttons := buildLyricsButtons(playbackKey, false, false, false, true, pbSession.IsDedicated, 0)
						_, _ = editLyricsMessage(s, nil, pbSession, currentResult.TrackName, currentResult.Artist, "🎶 **가사 재생 완료**", fFooter, buttons)
					}
					continue
				}

				if currentIdx != pbSession.LastIdx {
					busy = true
					pbSession.mu.Lock()
					pbSession.LastIdx = currentIdx
					pbSession.mu.Unlock()

					buttons := buildLyricsButtons(playbackKey, true, false, false, true, pbSession.IsDedicated, 0)
					desc := buildLyricsDescription(currentResult, currentIdx)
					footer := buildLyricsFooter(currentResult, elapsed)

					_, err := editLyricsMessage(s, nil, pbSession, currentResult.TrackName, currentResult.Artist, desc, footer, buttons)
					if err != nil {
						slog.Warn("Failed to edit auto lyrics", "error", err)
						pbSession.mu.Lock()
						pbSession.LastIdx = -1
						pbSession.mu.Unlock()
					}
					busy = false
				}
			}
		}
	}
}

func StartSpotifyRecoveryWorker(s *discordgo.Session) {
	if redisClient == nil {
		return
	}
	states := redisClient.LoadAllChannelSpotifyStates()
	if len(states) == 0 {
		slog.Info("No active Spotify channel sessions to recover on startup")
		return
	}

	slog.Info("Starting Spotify recovery worker for channel sessions", "count", len(states))
	for _, st := range states {
		if st == nil || st.ChannelID == "" || st.MessageID == "" {
			continue
		}

		if _, err := s.ChannelMessage(st.ChannelID, st.MessageID); err != nil {
			slog.Info("Recovered Spotify message no longer exists on Discord, deleting state", "channel_id", st.ChannelID, "message_id", st.MessageID)
			_ = redisClient.DeleteChannelSpotifyState(st.ChannelID)
			continue
		}

		playbackKey := st.PlaybackKey
		if playbackKey == "" {
			playbackKey = fmt.Sprintf("%d", time.Now().UnixNano())
		}

		pbSession := &LyricsPlaybackSession{
			Key:         playbackKey,
			OwnerID:     st.OwnerID,
			ChannelID:   st.ChannelID,
			MessageID:   st.MessageID,
			Done:        make(chan struct{}),
			IsAuto:      st.IsAuto,
			IsDedicated: st.IsDedicated,
			ImageMode:   st.ImageMode,
			FramesDir:   fmt.Sprintf("/tmp/lyrics_frames_%s", playbackKey),
		}

		activeLyricsPlaybacks.Store(playbackKey, pbSession)
		channelLyricsPlaybacks.Store(st.ChannelID, pbSession)

		slog.Info("Successfully recovered active Spotify channel session", "channel_id", st.ChannelID, "message_id", st.MessageID, "user_id", st.TargetUserID, "is_dedicated", st.IsDedicated)
		if (st.IsAuto || st.IsDedicated) && st.TargetUserID != "" {
			go runSpotifyAutoTrackingLoop(s, pbSession, st.TargetUserID, st.TargetUsername, st.GuildID)
		}
	}
}
