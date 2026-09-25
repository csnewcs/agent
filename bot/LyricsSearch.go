package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	spotifyTrackRegex = regexp.MustCompile(`(?:open\.spotify\.com/track/|spotify:track:)([a-zA-Z0-9]+)`)
	ytVideoRegex      = regexp.MustCompile(`(?:youtube\.com/watch\?v=|youtu\.be/)([a-zA-Z0-9_-]+)`)
	lrcLineRegex      = regexp.MustCompile(`^\[([0-9]+):([0-9]+\.?[0-9]*)\]\s*(.*)`)
)

var songTitleCleanRegexes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\s*[\(\[](?:feat\.|ft\.|with|cover|remix|version|ver\.|live|instrumental|inst\.|prod\.|off vocal).*?[\)\]]`),
	regexp.MustCompile(`(?i)\s*-\s*(?:feat\.|ft\.|with|cover|remix|version|ver\.|live|instrumental|inst\.|prod\.|off vocal).*?$`),
	regexp.MustCompile(`(?i)\s*-\s*.*?Live\s*-?$`),
}

var knownRomajiToKana = map[string]string{
	"override":                    "オーバーライド",
	"lagtrain":                    "ラグトレイン",
	"phony":                       "フォニイ",
	"god-ish":                     "神っぽいな",
	"mesmerizer":                  "メズマライザー",
	"rabbit hole":                 "ラビットホール",
	"vampire":                     "ヴァンパイア",
	"king":                        "KING",
	"envy baby":                   "エンヴィーベイビー",
	"unknown mother goose":        "アンノウン・マザーグース",
	"marshall maximizer":          "マーシャル・マキシマイザー",
	"kyu-kurarin":                 "きゅうくらりん",
	"kyukurarin":                  "きゅうくらりん",
	"goodbye declaration":         "グッバイ宣言",
	"shukusei!! loli-kami requiem": "粛聖!! 로리신 레퀴엠☆",
	"idol":                        "アイドル",
	"monster":                     "怪物",
	"racing into the night":       "夜に駆ける",
	"yoru ni kakeru":              "夜に駆ける",
	"gunjo":                       "群青",
	"kaibutsu":                    "怪物",
	"cinema":                      "シネマ",
	"bug":                         "バグ",
	"lower":                       "ロウワー",
	"gehenna":                     "ゲヘナ",
	"villain":                     "ヴィラン",
	"gimmexgimme":                 "ギミ×ギミ",
	"telecaster b-boy":            "テレキャスタービーボーイ",
	"venom":                       "ベノム",
	"bocca della verita":          "ボッカデラベリタ",
	"charles":                     "シャルル",
	"roshin yukai":                "炉心融解",
	"meltdown":                    "炉心融解",
	"world is mine":               "ワールドイズマイン",
	"melt":                        "メルト",
	"rollin girl":                 "ローリンガール",
	"rolling girl":                "ローリンガール",
	"two-faced lovers":            "裏表ラバーズ",
	"matryoshka":                  "マトリョシカ",
	"senbonzakura":                "千本桜",
	"shoushitsu":                  "初音ミクの消失",
}

func fmtMs(ms int) string {
	if ms < 0 {
		ms = 0
	}
	s := ms / 1000
	m := s / 60
	sec := s % 60
	return fmt.Sprintf("%02d:%02d", m, sec)
}

func parseLRC(syncedLyrics string) []LyricLine {
	var lines []LyricLine
	rawLines := strings.Split(syncedLyrics, "\n")
	for _, raw := range rawLines {
		raw = strings.TrimSpace(raw)
		m := lrcLineRegex.FindStringSubmatch(raw)
		if len(m) < 4 {
			continue
		}
		minutes, err1 := strconv.Atoi(m[1])
		seconds, err2 := strconv.ParseFloat(m[2], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		timeMs := int(float64(minutes*60000) + seconds*1000.0)
		text := strings.TrimSpace(m[3])
		if len(text) > 0 {
			lines = append(lines, LyricLine{
				TimeMs: timeMs,
				Text:   text,
			})
		}
	}
	return lines
}

func findCurrentLyricsIndex(lines []LyricLine, elapsedMs int) int {
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i].TimeMs <= elapsedMs {
			return i
		}
	}
	return 0
}

func normalizeMusicString(s string) string {
	s = strings.ReplaceAll(s, "（", "(")
	s = strings.ReplaceAll(s, "）", ")")
	s = strings.ReplaceAll(s, "【", "[")
	s = strings.ReplaceAll(s, "】", "]")
	s = strings.ReplaceAll(s, "　", " ")
	return strings.TrimSpace(s)
}

func cleanMusicTitle(title string) string {
	t := normalizeMusicString(title)
	for _, re := range songTitleCleanRegexes {
		t = re.ReplaceAllString(t, "")
	}
	return strings.TrimSpace(t)
}

func cleanMusicArtist(artist string) string {
	a := normalizeMusicString(artist)
	if idx := strings.Index(strings.ToLower(a), "feat."); idx != -1 {
		a = a[:idx]
	}
	if idx := strings.Index(strings.ToLower(a), "ft."); idx != -1 {
		a = a[:idx]
	}
	if idx := strings.Index(a, ","); idx != -1 {
		a = a[:idx]
	}
	if idx := strings.Index(a, "&"); idx != -1 {
		a = a[:idx]
	}
	return strings.TrimSpace(a)
}

func fetchYouTubeTitleAndCover(videoID string) (string, string) {
	oembedURL := fmt.Sprintf("https://www.youtube.com/oembed?url=https://www.youtube.com/watch?v=%s&format=json", videoID)
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(oembedURL)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", ""
	}
	var data struct {
		Title        string `json:"title"`
		ThumbnailURL string `json:"thumbnail_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err == nil {
		return data.Title, data.ThumbnailURL
	}
	return "", ""
}

func fetchSpotifyTrackInfo(trackID string) (string, string, string, int) {
	embedURL := fmt.Sprintf("https://open.spotify.com/embed/track/%s", trackID)
	client := http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", embedURL, nil)
	if err == nil {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			defer resp.Body.Close()
			var bodyBuf bytes.Buffer
			_, _ = bodyBuf.ReadFrom(resp.Body)
			html := bodyBuf.String()

			nextDataRegex := regexp.MustCompile(`<script id="__NEXT_DATA__"[^>]*>(.*?)</script>`)
			m := nextDataRegex.FindStringSubmatch(html)
			if len(m) > 1 {
				var data struct {
					Props struct {
						PageProps struct {
							State struct {
								Data struct {
									Entity struct {
										Name    string `json:"name"`
										Title   string `json:"title"`
										Artists []struct {
											Name string `json:"name"`
										} `json:"artists"`
										Duration       int `json:"duration"`
										VisualIdentity struct {
											Image []struct {
												URL string `json:"url"`
											} `json:"image"`
										} `json:"visualIdentity"`
									} `json:"entity"`
								} `json:"data"`
							} `json:"state"`
						} `json:"pageProps"`
					} `json:"props"`
				}
				if err := json.Unmarshal([]byte(m[1]), &data); err == nil {
					entity := data.Props.PageProps.State.Data.Entity
					trackName := entity.Name
					if trackName == "" {
						trackName = entity.Title
					}
					var artistNames []string
					for _, a := range entity.Artists {
						if a.Name != "" {
							artistNames = append(artistNames, a.Name)
						}
					}
					artistName := strings.Join(artistNames, ", ")
					coverURL := ""
					if len(entity.VisualIdentity.Image) > 0 {
						coverURL = entity.VisualIdentity.Image[0].URL
					}
					if trackName != "" {
						return trackName, artistName, coverURL, entity.Duration
					}
				}
			}
		}
	}

	oembedURL := fmt.Sprintf("https://open.spotify.com/oembed?url=https://open.spotify.com/track/%s", trackID)
	resp, err := client.Get(oembedURL)
	if err == nil && resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		var data struct {
			Title        string `json:"title"`
			ThumbnailURL string `json:"thumbnail_url"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&data); err == nil {
			return data.Title, "", data.ThumbnailURL, 0
		}
	}

	return "", "", "", 0
}

type LRCLIBTrackEntry struct {
	ID           int     `json:"id"`
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	AlbumName    string  `json:"albumName"`
	Duration     float64 `json:"duration"`
	Instrumental bool    `json:"instrumental"`
	PlainLyrics  string  `json:"plainLyrics"`
	SyncedLyrics string  `json:"syncedLyrics"`
}

func tryLRCLIBGet(client *http.Client, trackName, artistName string) *LyricResult {
	if trackName == "" || artistName == "" {
		return nil
	}
	params := url.Values{}
	params.Set("track_name", trackName)
	params.Set("artist_name", artistName)
	getURL := "https://lrclib.net/api/get?" + params.Encode()

	req, err := http.NewRequest("GET", getURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "AntigravityBot/1.0 (https://github.com/csnewcs/agent)")
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return nil
	}
	defer resp.Body.Close()

	var entry LRCLIBTrackEntry
	if err := json.NewDecoder(resp.Body).Decode(&entry); err == nil && (entry.SyncedLyrics != "" || entry.PlainLyrics != "") {
		return processLRCLIBEntry(&entry)
	}
	return nil
}

var nonAlphaNumRegex = regexp.MustCompile(`[^\p{L}\p{N}]+`)

func simplifyStr(s string) string {
	s = strings.ToLower(normalizeMusicString(s))
	return strings.TrimSpace(nonAlphaNumRegex.ReplaceAllString(s, " "))
}

func calculateMatchScore(entry LRCLIBTrackEntry, expectedTrack, expectedArtist, query string, allowFuzzy bool) int {
	if entry.SyncedLyrics == "" && entry.PlainLyrics == "" {
		return 0
	}

	eTrack := simplifyStr(cleanMusicTitle(entry.TrackName))
	eArtist := simplifyStr(cleanMusicArtist(entry.ArtistName))
	expTrack := simplifyStr(cleanMusicTitle(expectedTrack))
	expArtist := simplifyStr(cleanMusicArtist(expectedArtist))
	q := simplifyStr(query)

	baseScore := 0

	// Case 1: When expected track name is explicitly given (e.g. Spotify tracking or Spotify link)
	if expTrack != "" {
		if eTrack == expTrack {
			if expArtist == "" || eArtist == expArtist || strings.Contains(eArtist, expArtist) || strings.Contains(expArtist, eArtist) {
				baseScore = 400
			} else if allowFuzzy {
				baseScore = 150
			} else {
				baseScore = 0 // Different artist on strict mode
			}
		} else if strings.HasPrefix(eTrack, expTrack+" ") || strings.HasPrefix(eTrack, expTrack+"-") {
			if expArtist == "" || strings.Contains(eArtist, expArtist) || strings.Contains(expArtist, eArtist) {
				baseScore = 300
			} else if allowFuzzy {
				baseScore = 100
			}
		} else if kana, ok := knownRomajiToKana[expTrack]; ok && simplifyStr(kana) == eTrack {
			if expArtist == "" || strings.Contains(eArtist, expArtist) || strings.Contains(expArtist, eArtist) {
				baseScore = 350
			} else if allowFuzzy {
				baseScore = 120
			}
		} else if strings.Contains(eTrack, expTrack) || strings.Contains(expTrack, eTrack) {
			if expArtist == "" || strings.Contains(eArtist, expArtist) || strings.Contains(expArtist, eArtist) {
				baseScore = 180
			} else if allowFuzzy {
				baseScore = 80
			}
		} else if allowFuzzy {
			baseScore = 20
		}
	} else if q != "" {
		// Case 2: When user searched with free-form text query (e.g. "밤편지", "아이유 밤편지", "Monster YOASOBI")
		if eTrack == q {
			baseScore = 350
		} else if q == eTrack+" "+eArtist || q == eArtist+" "+eTrack {
			baseScore = 400
		} else if strings.Contains(q, eTrack) && eTrack != "" {
			if eArtist != "" && strings.Contains(q, eArtist) {
				baseScore = 380
			} else {
				baseScore = 280
			}
		} else if strings.HasPrefix(eTrack, q+" ") || strings.HasPrefix(eTrack, q+"-") {
			baseScore = 260
		} else if kana, ok := knownRomajiToKana[q]; ok && (eTrack == simplifyStr(kana) || strings.HasPrefix(eTrack, simplifyStr(kana)+" ")) {
			baseScore = 340
		} else if strings.Contains(eTrack, q) {
			baseScore = 120
		} else {
			// Word-level matching
			qWords := strings.Fields(q)
			matchCount := 0
			for _, w := range qWords {
				if len(w) > 0 && (strings.Contains(eTrack, w) || strings.Contains(eArtist, w)) {
					matchCount++
				}
			}
			if len(qWords) > 0 && matchCount == len(qWords) {
				baseScore = 250
			} else if matchCount > 0 {
				baseScore = 60 + (matchCount * 20)
			} else if allowFuzzy {
				baseScore = 20
			}
		}
	}

	if baseScore > 0 && entry.SyncedLyrics != "" {
		baseScore += 50
	}

	return baseScore
}

func tryLRCLIBSearchWithMatch(client *http.Client, params url.Values, expectedTrack, expectedArtist, query string, allowFuzzy bool) *LyricResult {
	searchURL := "https://lrclib.net/api/search?" + params.Encode()
	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "AntigravityBot/1.0 (https://github.com/csnewcs/agent)")
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return nil
	}
	defer resp.Body.Close()

	var entries []LRCLIBTrackEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil || len(entries) == 0 {
		return nil
	}

	var bestEntry *LRCLIBTrackEntry
	highestScore := 0

	for i := range entries {
		score := calculateMatchScore(entries[i], expectedTrack, expectedArtist, query, allowFuzzy)
		if score > highestScore {
			highestScore = score
			bestEntry = &entries[i]
		}
	}

	if bestEntry != nil && highestScore > 0 {
		return processLRCLIBEntry(bestEntry)
	}
	return nil
}

func fetchLyricsFromLRCLIB(query, trackName, artistName string, allowFuzzy bool) (*LyricResult, error) {
	client := &http.Client{Timeout: 8 * time.Second}

	cleanTrack := cleanMusicTitle(trackName)
	cleanArtist := cleanMusicArtist(artistName)

	// 1. Exact GET with original names (when track and artist are known)
	if trackName != "" && artistName != "" {
		if res := tryLRCLIBGet(client, trackName, artistName); res != nil {
			return res, nil
		}
	}

	// 2. Exact GET with cleaned names
	if cleanTrack != "" && cleanArtist != "" && (cleanTrack != trackName || cleanArtist != artistName) {
		if res := tryLRCLIBGet(client, cleanTrack, cleanArtist); res != nil {
			return res, nil
		}
	}

	// 3. Search with track_name and artist_name
	if cleanTrack != "" && cleanArtist != "" {
		p := url.Values{}
		p.Set("track_name", cleanTrack)
		p.Set("artist_name", cleanArtist)
		if res := tryLRCLIBSearchWithMatch(client, p, cleanTrack, cleanArtist, query, allowFuzzy); res != nil {
			return res, nil
		}
	}

	// 4. Search with combined track + artist query
	if cleanTrack != "" && cleanArtist != "" {
		p := url.Values{}
		p.Set("q", cleanTrack+" "+cleanArtist)
		if res := tryLRCLIBSearchWithMatch(client, p, cleanTrack, cleanArtist, query, allowFuzzy); res != nil {
			return res, nil
		}
	}

	// 5. Search with Japanese Kana mapping + artist
	if kana, ok := knownRomajiToKana[strings.ToLower(cleanTrack)]; ok && kana != "" && cleanArtist != "" {
		p := url.Values{}
		p.Set("q", kana+" "+cleanArtist)
		if res := tryLRCLIBSearchWithMatch(client, p, cleanTrack, cleanArtist, query, allowFuzzy); res != nil {
			return res, nil
		}
	}

	// 6. Text query search (with smart scoring to prevent wrong songs)
	searchQ := query
	if searchQ == "" {
		if cleanTrack != "" && cleanArtist != "" {
			searchQ = cleanTrack + " " + cleanArtist
		} else if cleanTrack != "" {
			searchQ = cleanTrack
		}
	}

	if searchQ != "" {
		p := url.Values{}
		p.Set("q", searchQ)
		if res := tryLRCLIBSearchWithMatch(client, p, cleanTrack, cleanArtist, searchQ, allowFuzzy); res != nil {
			return res, nil
		}

		if kana, ok := knownRomajiToKana[strings.ToLower(strings.TrimSpace(searchQ))]; ok && kana != "" {
			pKana := url.Values{}
			pKana.Set("q", kana)
			if res := tryLRCLIBSearchWithMatch(client, pKana, cleanTrack, cleanArtist, searchQ, allowFuzzy); res != nil {
				return res, nil
			}
		}

		if strings.Contains(searchQ, " - ") {
			parts := strings.SplitN(searchQ, " - ", 2)
			p1 := strings.TrimSpace(parts[0])
			p2 := strings.TrimSpace(parts[1])
			if p1 != "" && p2 != "" {
				if res := tryLRCLIBGet(client, p2, p1); res != nil {
					return res, nil
				}
				if res := tryLRCLIBGet(client, p1, p2); res != nil {
					return res, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("가사를 찾을 수 없습니다")
}

func processLRCLIBEntry(bestEntry *LRCLIBTrackEntry) *LyricResult {
	durationMs := int(bestEntry.Duration * 1000)
	var lines []LyricLine

	if bestEntry.SyncedLyrics != "" {
		lines = parseLRC(bestEntry.SyncedLyrics)
	} else if bestEntry.PlainLyrics != "" {
		plain := strings.Split(bestEntry.PlainLyrics, "\n")
		var nonBlank []string
		for _, pl := range plain {
			pl = strings.TrimSpace(pl)
			if pl != "" {
				nonBlank = append(nonBlank, pl)
			}
		}
		if len(nonBlank) > 0 {
			stepMs := durationMs / (len(nonBlank) + 1)
			if stepMs <= 0 {
				stepMs = 3000
			}
			for i, t := range nonBlank {
				lines = append(lines, LyricLine{
					TimeMs: (i + 1) * stepMs,
					Text:   t,
				})
			}
		}
	}

	if durationMs <= 0 && len(lines) > 0 {
		durationMs = lines[len(lines)-1].TimeMs + 10000
	}

	return &LyricResult{
		Source:     "lrclib",
		TrackName:  bestEntry.TrackName,
		Artist:     bestEntry.ArtistName,
		Album:      bestEntry.AlbumName,
		DurationMs: durationMs,
		Lines:      lines,
	}
}

type PetitLyricsResponse struct {
	XMLName    xml.Name `xml:"response"`
	Status     int      `xml:"status"`
	Message    string   `xml:"message"`
	LyricsData string   `xml:"songs>song>lyricsData"`
	Title      string   `xml:"songs>song>title"`
	Artist     string   `xml:"songs>song>artist"`
}

type PetitWSYXML struct {
	XMLName xml.Name       `xml:"wsy"`
	Lines   []PetitWSYLine `xml:"line"`
}

type PetitWSYLine struct {
	LineString string         `xml:"linestring"`
	Words      []PetitWSYWord `xml:"word"`
}

type PetitWSYWord struct {
	StartTime int `xml:"starttime"`
}

func tryPetitLyricsSingle(client *http.Client, title, artist string) *LyricResult {
	if title == "" {
		return nil
	}

	data := url.Values{}
	data.Set("lyricsType", "3")
	data.Set("sdkVer", "1.3.4")
	data.Set("userId", "642bbdc6-128a-4b67-a10b-bc09191b0cfe")
	data.Set("appName", "HF Player")
	data.Set("pkgName", "com.onkyo.jp.musicplayer")
	data.Set("clientAppId", "on354007")
	data.Set("index", "0")
	data.Set("logFlag", "0")
	data.Set("verCode", "212")
	data.Set("verName", "2.7.0")
	data.Set("maxcount", "1")
	data.Set("terminalType", "0")
	data.Set("key_title", title)
	if artist != "" {
		data.Set("key_artist", artist)
	}

	req, err := http.NewRequest("POST", "https://on.petitlyrics.com/api/GetPetitLyricsData.php", strings.NewReader(data.Encode()))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Header.Set("User-Agent", "Dalvik/2.1.0 (Linux; U; Android 5.1.1; LM-G820UM Build/LMY48Z)")

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return nil
	}
	defer resp.Body.Close()

	var plResp PetitLyricsResponse
	if err := xml.NewDecoder(resp.Body).Decode(&plResp); err != nil || plResp.LyricsData == "" {
		return nil
	}

	pTitleClean := simplifyStr(cleanMusicTitle(plResp.Title))
	reqTitleClean := simplifyStr(cleanMusicTitle(title))

	isTitleMatch := false
	if pTitleClean == reqTitleClean {
		isTitleMatch = true
	} else if kana, ok := knownRomajiToKana[reqTitleClean]; ok && simplifyStr(kana) == pTitleClean {
		isTitleMatch = true
	} else if strings.HasPrefix(pTitleClean, reqTitleClean+" ") || strings.HasPrefix(pTitleClean, reqTitleClean+"-") {
		isTitleMatch = true
	}

	if !isTitleMatch {
		return nil
	}

	if artist != "" {
		pArtist := simplifyStr(cleanMusicArtist(plResp.Artist))
		reqArtist := simplifyStr(cleanMusicArtist(artist))
		if reqArtist != "" && pArtist != reqArtist && !strings.Contains(pArtist, reqArtist) && !strings.Contains(reqArtist, pArtist) {
			return nil
		}
	}

	decodedBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(plResp.LyricsData))
	if err != nil {
		return nil
	}

	var wsy PetitWSYXML
	if err := xml.Unmarshal(decodedBytes, &wsy); err != nil {
		return nil
	}

	var lines []LyricLine
	for _, l := range wsy.Lines {
		txt := strings.TrimSpace(l.LineString)
		if txt == "" {
			continue
		}
		timeMs := 0
		if len(l.Words) > 0 {
			timeMs = l.Words[0].StartTime
		}
		lines = append(lines, LyricLine{
			TimeMs: timeMs,
			Text:   txt,
		})
	}

	if len(lines) == 0 {
		return nil
	}

	durationMs := lines[len(lines)-1].TimeMs + 5000
	tName := plResp.Title
	if tName == "" {
		tName = title
	}
	art := plResp.Artist
	if art == "" {
		art = artist
	}

	return &LyricResult{
		Source:     "petitlyrics",
		TrackName:  tName,
		Artist:     art,
		DurationMs: durationMs,
		Lines:      lines,
	}
}

func fetchLyricsFromPetitLyrics(trackName, artistName string, allowFuzzy bool) (*LyricResult, error) {
	if trackName == "" {
		return nil, fmt.Errorf("track name empty")
	}
	client := &http.Client{Timeout: 6 * time.Second}

	cleanTrack := cleanMusicTitle(trackName)
	cleanArtist := cleanMusicArtist(artistName)

	// 1. Strict title + artist (when artist is provided)
	if cleanArtist != "" {
		if res := tryPetitLyricsSingle(client, trackName, artistName); res != nil {
			return res, nil
		}
		if cleanTrack != trackName || cleanArtist != artistName {
			if res := tryPetitLyricsSingle(client, cleanTrack, cleanArtist); res != nil {
				return res, nil
			}
		}
		if kana, ok := knownRomajiToKana[strings.ToLower(cleanTrack)]; ok && kana != "" {
			if res := tryPetitLyricsSingle(client, kana, cleanArtist); res != nil {
				return res, nil
			}
		}
	}

	// 2. Exact Title search (ONLY when artist is empty OR allowFuzzy is true)
	if cleanArtist == "" || allowFuzzy {
		if res := tryPetitLyricsSingle(client, cleanTrack, ""); res != nil {
			return res, nil
		}
		if kana, ok := knownRomajiToKana[strings.ToLower(cleanTrack)]; ok && kana != "" {
			if res := tryPetitLyricsSingle(client, kana, ""); res != nil {
				return res, nil
			}
		}

		// 3. Multi-word title/artist separation (e.g. "YOASOBI 怪物" or "怪物 YOASOBI")
		words := strings.Fields(cleanTrack)
		if len(words) == 2 {
			if res := tryPetitLyricsSingle(client, words[1], words[0]); res != nil {
				return res, nil
			}
			if res := tryPetitLyricsSingle(client, words[0], words[1]); res != nil {
				return res, nil
			}
			if kana, ok := knownRomajiToKana[strings.ToLower(words[0])]; ok && kana != "" {
				if res := tryPetitLyricsSingle(client, kana, words[1]); res != nil {
					return res, nil
				}
			}
			if kana, ok := knownRomajiToKana[strings.ToLower(words[1])]; ok && kana != "" {
				if res := tryPetitLyricsSingle(client, kana, words[0]); res != nil {
					return res, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("petitlyrics returned 0 lines")
}

func fetchLyricsConcurrent(searchQuery, trackTitle, artistName string, allowFuzzy bool) (*LyricResult, error) {
	cacheKey := fmt.Sprintf("%s|%s", strings.ToLower(strings.TrimSpace(trackTitle)), strings.ToLower(strings.TrimSpace(artistName)))
	if cacheKey != "|" {
		if cached := getCachedLyrics(cacheKey); cached != nil {
			return cached, nil
		}
	}

	type searchOutcome struct {
		result *LyricResult
		err    error
	}

	ch := make(chan searchOutcome, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	go func() {
		res, err := fetchLyricsFromLRCLIB(searchQuery, trackTitle, artistName, allowFuzzy)
		ch <- searchOutcome{result: res, err: err}
	}()

	go func() {
		pTitle := trackTitle
		if pTitle == "" {
			pTitle = searchQuery
		}
		res, err := fetchLyricsFromPetitLyrics(pTitle, artistName, allowFuzzy)
		ch <- searchOutcome{result: res, err: err}
	}()

	var firstErr error
	for i := 0; i < 2; i++ {
		select {
		case out := <-ch:
			if out.err == nil && out.result != nil && len(out.result.Lines) > 0 {
				cancel()
				if cacheKey != "|" {
					setCachedLyrics(cacheKey, out.result)
				}
				return out.result, nil
			}
			if firstErr == nil && out.err != nil {
				firstErr = out.err
			}
		case <-ctx.Done():
			if firstErr != nil {
				return nil, firstErr
			}
			return nil, fmt.Errorf("가사 검색 시간이 초과되었습니다")
		}
	}

	return nil, fmt.Errorf("가사를 찾을 수 없습니다")
}
