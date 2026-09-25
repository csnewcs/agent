package main

import (
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Live provider checks are opt-in: they depend on external services, rate limits,
// and credentials, so they must not make the ordinary unit-test suite flaky.
func requireLyricsIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("LYRICS_INTEGRATION") != "1" {
		t.Skip("set LYRICS_INTEGRATION=1 to run live lyrics provider checks")
	}
}

func requireGeminiIntegrationKey(t *testing.T) string {
	t.Helper()
	requireLyricsIntegration(t)
	key := os.Getenv("GEMINI_API_KEY")
	if key == "" {
		t.Skip("set GEMINI_API_KEY to run live Gemini checks")
	}
	return key
}

func TestParseLRC(t *testing.T) {
	sampleLRC := `[00:05.12] First line
[00:12.80] Second line with text
[01:04.50] Third line later
`
	lines := parseLRC(sampleLRC)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if lines[0].TimeMs != 5120 || lines[0].Text != "First line" {
		t.Errorf("line 0 mismatch: %v", lines[0])
	}
	if lines[1].TimeMs != 12800 || lines[1].Text != "Second line with text" {
		t.Errorf("line 1 mismatch: %v", lines[1])
	}
	if lines[2].TimeMs != 64500 || lines[2].Text != "Third line later" {
		t.Errorf("line 2 mismatch: %v", lines[2])
	}

	idx0 := findCurrentLyricsIndex(lines, 0)
	if idx0 != 0 {
		t.Errorf("expected idx 0 for 0ms, got %d", idx0)
	}

	idx1 := findCurrentLyricsIndex(lines, 6000)
	if idx1 != 0 {
		t.Errorf("expected idx 0 for 6000ms, got %d", idx1)
	}

	idx2 := findCurrentLyricsIndex(lines, 15000)
	if idx2 != 1 {
		t.Errorf("expected idx 1 for 15000ms, got %d", idx2)
	}

	idx3 := findCurrentLyricsIndex(lines, 70000)
	if idx3 != 2 {
		t.Errorf("expected idx 2 for 70000ms, got %d", idx3)
	}
}

func TestKatakanaToHiragana(t *testing.T) {
	kata := "カタカナ"
	hira := katakanaToHiragana(kata)
	if hira != "かたかな" {
		t.Errorf("expected かたかな, got %s", hira)
	}
}

func TestSpecificSpotifyTrack(t *testing.T) {
	requireLyricsIntegration(t)
	trackID := "0K7TLVKmjmDWtdtRPqdryD"
	title, artist, cover, duration := fetchSpotifyTrackInfo(trackID)
	t.Logf("Spotify info: Title='%s', Artist='%s', Cover='%s', Duration=%d", title, artist, cover, duration)

	searchQ := title
	if artist != "" {
		searchQ = title + " " + artist
	}
	res, err := fetchLyricsConcurrent(searchQ, title, artist, false)
	if err != nil {
		t.Logf("fetchLyricsConcurrent strict returned err: %v", err)
	} else {
		t.Logf("fetchLyricsConcurrent strict found: Title='%s', Artist='%s', Source='%s', Lines=%d",
			res.TrackName, res.Artist, res.Source, len(res.Lines))
	}
}

func TestTranslateWithGoogle(t *testing.T) {
	requireLyricsIntegration(t)
	lines := []LyricLine{
		{TimeMs: 1000, Text: "Hello world"},
		{TimeMs: 3000, Text: "I love you"},
	}
	lang, trans := translateWithGoogle(lines)
	if lang == "" || lang == "unknown" {
		t.Logf("detected lang: %s", lang)
	}
	if len(trans) != 2 {
		t.Fatalf("expected 2 translated lines, got %d", len(trans))
	}
	t.Logf("Translated: %v, Lang: %s", trans, lang)
}

func TestTranslateWithGemini(t *testing.T) {
	apiKey := requireGeminiIntegrationKey(t)
	lines := []LyricLine{
		{TimeMs: 1000, Text: "When the twilight fades away"},
		{TimeMs: 3000, Text: "The winds howl a wistful tune"},
	}
	lang, trans, pron, model, err := translateWithGemini(lines, "Goodbye", "The 1999", apiKey)
	if err != nil {
		t.Fatalf("Gemini translation error: %v", err)
	}
	if len(trans) != 2 || len(pron) != 2 {
		t.Fatalf("unexpected length trans: %d, pron: %d", len(trans), len(pron))
	}
	t.Logf("Gemini result: Lang=%s, Model=%s, Trans=%v, Pron=%v", lang, model, trans, pron)
}

func TestFetchLyricsFromPetitLyrics(t *testing.T) {
	requireLyricsIntegration(t)
	res, err := fetchLyricsFromPetitLyrics("怪物", "YOASOBI", false)
	if err != nil {
		t.Fatalf("PetitLyrics fetch failed: %v", err)
	}
	if len(res.Lines) == 0 {
		t.Fatalf("Expected lines, got 0")
	}
	t.Logf("PetitLyrics fetched: %s by %s, %d lines, first line: %v", res.TrackName, res.Artist, len(res.Lines), res.Lines[0])
}

func TestUniversalJSONParser(t *testing.T) {
	sample1 := `{"lang": "ja", "translations": ["안녕", "세상"], "pronunciations": ["안녕", "세상"]}`
	l1, t1, p1, err1 := parseLyricsAIResponse(sample1, 2)
	if err1 != nil || len(t1) != 2 || t1[0] != "안녕" || p1[1] != "세상" {
		t.Fatalf("Format 1 failed: %v, %v, %v, err=%v", l1, t1, p1, err1)
	}

	sample2 := `[{"index": 9, "text": "더는 참지 말고 다 털어놔 줘"}, {"index": 10, "text": "MWAH!"}]`
	l2, t2, _, err2 := parseLyricsAIResponse(sample2, 2)
	if err2 != nil || len(t2) != 2 || t2[0] != "더는 참지 말고 다 털어놔 줘" {
		t.Fatalf("Format 2 failed: %v, %v, err=%v", l2, t2, err2)
	}
}

func TestFetchLyricsMonitoring(t *testing.T) {
	apiKey := requireGeminiIntegrationKey(t)
	res, err := fetchLyricsConcurrent("モニタリング DECO*27", "モニタリング", "DECO*27", false)
	if err != nil {
		t.Fatalf("fetchLyricsConcurrent failed: %v", err)
	}
	t.Logf("Monitoring fetched: %s by %s, total lines: %d, source: %s", res.TrackName, res.Artist, len(res.Lines), res.Source)

	attachLyricsTranslation(res, apiKey, "")

	if len(res.TranslatedLines) != len(res.Lines) {
		t.Fatalf("expected %d translated lines, got %d", len(res.Lines), len(res.TranslatedLines))
	}
	if len(res.PronunciationLines) != len(res.Lines) {
		t.Fatalf("expected %d pronunciation lines, got %d", len(res.Lines), len(res.PronunciationLines))
	}

	lastLineIdx := len(res.Lines) - 1
	if res.TranslatedLines[lastLineIdx] == "" {
		t.Errorf("last line (%d) translation is empty", lastLineIdx)
	}
	if res.PronunciationLines[lastLineIdx] == "" {
		t.Errorf("last line (%d) pronunciation is empty", lastLineIdx)
	}

	if res.TranslationModel == "" {
		t.Errorf("expected TranslationModel to be set, got empty")
	}
	if res.DetectedLang == "" {
		t.Errorf("expected DetectedLang to be set, got empty")
	}
	t.Logf("TranslationModel used: %s (%s)", res.TranslationModel, res.DetectedLang)

	t.Logf("Line 0: [%s] -> [%s] (%s)", res.Lines[0].Text, res.TranslatedLines[0], res.PronunciationLines[0])
	if len(res.Lines) > 85 {
		t.Logf("Line 85: [%s] -> [%s] (%s)", res.Lines[85].Text, res.TranslatedLines[85], res.PronunciationLines[85])
	}
	t.Logf("Line %d: [%s] -> [%s] (%s)", lastLineIdx, res.Lines[lastLineIdx].Text, res.TranslatedLines[lastLineIdx], res.PronunciationLines[lastLineIdx])
}

func TestFetchLyricsTianTian(t *testing.T) {
	apiKey := requireGeminiIntegrationKey(t)
	res, err := fetchLyricsConcurrent("TIAN TIAN", "TIAN TIAN", "", true)
	if err != nil {
		t.Fatalf("fetchLyricsConcurrent failed: %v", err)
	}
	t.Logf("Fetched: %s by %s, total lines: %d, source: %s", res.TrackName, res.Artist, len(res.Lines), res.Source)
	for i, l := range res.Lines {
		t.Logf("Raw Line %d: %s", i, l.Text)
	}

	attachLyricsTranslation(res, apiKey, "")

	for i := 0; i < len(res.Lines); i++ {
		tr := ""
		if i < len(res.TranslatedLines) {
			tr = res.TranslatedLines[i]
		}
		pr := ""
		if i < len(res.PronunciationLines) {
			pr = res.PronunciationLines[i]
		}
		t.Logf("[%02d] Original: %s | Trans: %s | Pron: %s", i, res.Lines[i].Text, tr, pr)
	}
}

func TestRenderLyricsCentered(t *testing.T) {
	outDir := t.TempDir()
	linesBeforeTrans := []RenderLyricLine{
		{Text: "Intro instrumental break...", TimeMs: 0},
		{Text: "Past line 1", TimeMs: 5000},
		{Text: "Past line 2", TimeMs: 10000},
		{Text: "Active line without translation yet", TimeMs: 15000},
		{Text: "Next upcoming line 1", TimeMs: 20000},
		{Text: "Next upcoming line 2", TimeMs: 25000},
	}
	outDir1 := filepath.Join(outDir, "before")
	err := RenderLyricsFramesNative("", 30000, linesBeforeTrans, outDir1)
	if err != nil {
		t.Fatalf("Render before translation failed: %v", err)
	}

	linesWithTrans := []RenderLyricLine{
		{Text: "Intro instrumental break...", TimeMs: 0},
		{Text: "Past line 1", TimeMs: 5000},
		{Text: "Past line 2", TimeMs: 10000},
		{Text: "Active line with Korean translation", Trans: "한국어 번역 가사가 아래에 표시되는 현재 줄", TimeMs: 15000},
		{Text: "Next upcoming line 1", TimeMs: 20000},
		{Text: "Next upcoming line 2", TimeMs: 25000},
	}
	outDir2 := filepath.Join(outDir, "with_trans")
	err = RenderLyricsFramesNative("", 30000, linesWithTrans, outDir2)
	if err != nil {
		t.Fatalf("Render with translation failed: %v", err)
	}

	linesWithPhonetic := []RenderLyricLine{
		{Text: "Intro instrumental break...", TimeMs: 0},
		{Text: "Past line 1", TimeMs: 5000},
		{Text: "Past line 2", TimeMs: 10000},
		{Text: "夜に駆ける 沈むように溶けてゆくように", Trans: "밤을 달리다 가라앉듯이 녹아내리듯이", Phonetic: "요루니 카케루 시즈무요오니 토케테유쿠요오니", TimeMs: 15000},
		{Text: "Next upcoming line 1", TimeMs: 20000},
		{Text: "Next upcoming line 2", TimeMs: 25000},
	}
	outDir3 := filepath.Join(outDir, "with_phonetic")
	err = RenderLyricsFramesNative("", 30000, linesWithPhonetic, outDir3)
	if err != nil {
		t.Fatalf("Render with phonetic failed: %v", err)
	}
}

func TestRenderLyricsLongLineTruncation(t *testing.T) {
	cfMain := newChainedFontFace(60)
	instMain := cfMain.NewInstance()

	longLine := "이것은 매우 매우 매우 매우 매우 매우 매우 매우 매우 매우 매우 매우 매우 매우 매우 매우 매우 매우 매우 매우 매우 긴 가사 라인입니다."
	maxW := 1430.0

	truncated := instMain.TruncateWithEllipsis(longLine, maxW)
	if !strings.HasSuffix(truncated, "...") {
		t.Errorf("Expected truncated string to end with '...', got: %s", truncated)
	}
	w, _ := instMain.MeasureString(truncated)
	if w > maxW {
		t.Errorf("Truncated string width %.2f exceeds max width %.2f", w, maxW)
	}
	t.Logf("Original: %s", longLine)
	t.Logf("Truncated: %s (width: %.2f / %.2f)", truncated, w, maxW)

	linesLong := []RenderLyricLine{
		{
			Text:     "아주 아주 아주 아주 아주 아주 아주 아주 아주 아주 아주 아주 아주 아주 아주 긴 원문 가사 라인 테스트",
			Trans:    "Very very very very very very very very very very very very long translated English lyrics test",
			Phonetic: "아주 아주 아주 아주 아주 아주 아주 아주 아주 아주 아주 아주 아주 아주 아주 긴 한글 발음 테스트",
			TimeMs:   10000,
		},
	}
	outDir := filepath.Join(t.TempDir(), "long_truncate")
	err := RenderLyricsFramesNative("", 20000, linesLong, outDir)
	if err != nil {
		t.Fatalf("Render with long lines failed: %v", err)
	}
}

func TestRenderNotFoundLyricsFrame(t *testing.T) {
	outDir := t.TempDir()
	outPathDark := filepath.Join(outDir, "not_found_dark.jpg")
	err := RenderNotFoundLyricsFrame("", outPathDark)
	if err != nil {
		t.Fatalf("RenderNotFoundLyricsFrame (dark) failed: %v", err)
	}

	cover := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			cover.Set(x, y, color.RGBA{R: 180, G: 60, B: 90, A: 255})
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		if err := png.Encode(w, cover); err != nil {
			t.Errorf("encode test cover: %v", err)
		}
	}))
	defer server.Close()

	outPathCover := filepath.Join(outDir, "not_found_cover.jpg")
	if err := RenderNotFoundLyricsFrame(server.URL, outPathCover); err != nil {
		t.Fatalf("RenderNotFoundLyricsFrame (cover) failed: %v", err)
	}
}

func TestFetchLyricsConcurrentResilience(t *testing.T) {
	requireLyricsIntegration(t)
	testCases := []struct {
		Query       string
		Track       string
		Artist      string
		ShouldMatch bool
	}{
		{Track: "OVERRIDE", Artist: "吉田夜世", ShouldMatch: true},
		{Track: "初音ミクの消失（feat.石川綾子）", Artist: "cosMo@Bousou-P", ShouldMatch: true},
		{Track: "トリノコシティ (feat. 桐谷遥&桃井愛莉&初音ミク)", Artist: "More More Jump!", ShouldMatch: true},
		{Query: "밤편지", ShouldMatch: true},
		{Query: "아이유 밤편지", ShouldMatch: true},
		{Query: "IU - 밤편지", ShouldMatch: true},
		{Query: "怪物", ShouldMatch: true},
		{Query: "YOASOBI 怪物", ShouldMatch: true},
		{Query: "monster", ShouldMatch: true},
		{Query: "Shape of You", ShouldMatch: true},
		{Query: "NonExistentSong999888ZZZ_RandomTest", ShouldMatch: false},
	}

	for _, tc := range testCases {
		query := tc.Query
		if query == "" {
			query = tc.Track
			if tc.Artist != "" {
				query = tc.Track + " " + tc.Artist
			}
		}
		res, err := fetchLyricsConcurrent(query, tc.Track, tc.Artist, false)
		if tc.ShouldMatch {
			if err != nil || res == nil || len(res.Lines) == 0 {
				t.Errorf("Failed to find lyrics for Query='%s', Track='%s', Artist='%s': %v", query, tc.Track, tc.Artist, err)
			} else {
				t.Logf("SUCCESS: Query='%s' -> Found via %s: '%s' by '%s' (%d lines)",
					query, res.Source, res.TrackName, res.Artist, len(res.Lines))
			}
		} else {
			if err == nil && res != nil && len(res.Lines) > 0 {
				t.Errorf("Expected failure for non-existent Query='%s', but got: '%s' by '%s'", query, res.TrackName, res.Artist)
			} else {
				t.Logf("CORRECTLY REJECTED: Query='%s' correctly returned not found", query)
			}
		}
	}
}

func TestLyricsQueueEnqueueDequeue(t *testing.T) {
	pbSession := &LyricsPlaybackSession{
		Key:  "test_queue_session",
		Done: make(chan struct{}),
	}

	item, qLen := pbSession.Enqueue("밤편지")
	if qLen != 1 {
		t.Fatalf("Expected queue length 1, got %d", qLen)
	}

	dequeued, ok := pbSession.Dequeue()
	if !ok || dequeued != item {
		t.Fatalf("Expected to dequeue the queued item")
	}
	if pbSession.QueueLength() != 0 {
		t.Fatalf("Expected queue length 0 after dequeue")
	}
}

func TestQueuePreloadingAndPretranslation(t *testing.T) {
	requireLyricsIntegration(t)
	pbSession := &LyricsPlaybackSession{
		Key:       "test_queue_session",
		ImageMode: true,
		Done:      make(chan struct{}),
	}
	item, _ := pbSession.Enqueue("밤편지")

	// Launch preload in background
	go preloadQueuedLyricsItem(pbSession, item)

	// Wait for preload completion
	select {
	case <-item.ReadyChan:
		if item.Err != nil {
			t.Fatalf("Preload failed with error: %v", item.Err)
		}
		if item.Result == nil || len(item.Result.Lines) == 0 {
			t.Fatalf("Preload resulted in empty lines")
		}
		t.Logf("Preloaded successfully: %s by %s, lines=%d, framesDir=%s",
			item.Result.TrackName, item.Result.Artist, len(item.Result.Lines), item.FramesDir)
		if item.FramesDir != "" {
			t.Cleanup(func() { _ = os.RemoveAll(item.FramesDir) })
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("Preload timed out after 15s")
	}

	dequeued, ok := pbSession.Dequeue()
	if !ok || dequeued != item {
		t.Fatalf("Expected to dequeue the preloaded item")
	}
	if pbSession.QueueLength() != 0 {
		t.Fatalf("Expected queue length 0 after dequeue, got %d", pbSession.QueueLength())
	}
}

func TestFormatResetTimeKST(t *testing.T) {
	// 07:27 UTC -> 16:27 KST, preserving the reset date.
	utcStr := "2026-08-23T07:27:36Z"
	res := formatResetTime(utcStr)
	if res != "2026-08-23 16:27 KST" {
		t.Errorf("Expected '2026-08-23 16:27 KST', got '%s'", res)
	}

	// Millisecond timestamps use the same date-inclusive format.
	utcNano := "2026-08-23T07:27:36.000Z"
	res2 := formatResetTime(utcNano)
	if res2 != "2026-08-23 16:27 KST" {
		t.Errorf("Expected '2026-08-23 16:27 KST', got '%s'", res2)
	}
}

func TestSpotifyCalibrationAndMatching(t *testing.T) {
	// 1. Test isTrackMatching
	if !isTrackMatching("밤편지", "아이유", "밤편지", "IU") {
		t.Errorf("expected isTrackMatching to match '밤편지'")
	}
	if !isTrackMatching("Monster (怪物)", "YOASOBI", "怪物", "YOASOBI") {
		t.Errorf("expected isTrackMatching to match 'Monster (怪物)' with '怪物'")
	}
	if isTrackMatching("Dynamite", "BTS", "Butter", "BTS") {
		t.Errorf("expected isTrackMatching to reject different songs")
	}

	// 2. Test CalibrateWithSpotifyActivity
	baseTime := time.Now().Add(-30 * time.Second)
	pbSession := &LyricsPlaybackSession{
		Key:       "test_calibrate",
		StartTime: baseTime,
		LastIdx:   3,
	}

	// Moderate drift: 400ms ahead
	actDrift := &discordgo.Activity{
		Details: "밤편지",
		State:   "아이유",
		Timestamps: discordgo.TimeStamps{
			StartTimestamp: baseTime.Add(400 * time.Millisecond).UnixMilli(),
		},
	}
	largeDrift := CalibrateWithSpotifyActivity(pbSession, actDrift, "밤편지", "아이유")
	if largeDrift {
		t.Errorf("400ms drift should be smooth (not large drift)")
	}
	expectedStart := time.UnixMilli(actDrift.Timestamps.StartTimestamp)
	if !pbSession.StartTime.Equal(expectedStart) {
		t.Errorf("pbSession.StartTime was not calibrated to 400ms drift")
	}

	// Large drift: 5000ms (Seek or Pause/Resume)
	actSeek := &discordgo.Activity{
		Details: "밤편지",
		State:   "아이유",
		Timestamps: discordgo.TimeStamps{
			StartTimestamp: baseTime.Add(-5000 * time.Millisecond).UnixMilli(),
		},
	}
	largeDrift2 := CalibrateWithSpotifyActivity(pbSession, actSeek, "밤편지", "아이유")
	if !largeDrift2 {
		t.Errorf("5000ms drift should trigger large drift recalibration")
	}
	if pbSession.LastIdx != -1 {
		t.Errorf("LastIdx should be reset to -1 on large drift recalibration")
	}
}
