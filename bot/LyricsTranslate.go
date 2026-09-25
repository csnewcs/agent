package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var hiraToKo = map[string]string{
	"あ": "아", "い": "이", "う": "우", "え": "에", "お": "오",
	"か": "카", "き": "키", "く": "쿠", "け": "케", "こ": "코",
	"が": "가", "ぎ": "기", "ぐ": "구", "게": "게", "ご": "고",
	"さ": "사", "し": "시", "す": "스", "せ": "세", "소": "소",
	"ざ": "자", "じ": "지", "ず": "즈", "ぜ": "제", "조": "조",
	"た": "타", "ち": "치", "つ": "츠", "て": "테", "と": "토",
	"だ": "다", "ぢ": "지", "づ": "즈", "데": "데", "도": "도",
	"な": "나", "に": "니", "ぬ": "누", "네": "네", "の": "노",
	"は": "하", "ひ": "히", "ふ": "후", "へ": "헤", "ほ": "호",
	"ば": "바", "び": "비", "ぶ": "부", "베": "베", "ぼ": "보",
	"ぱ": "파", "ぴ": "피", "ぷ": "푸", "ぺ": "페", "ぽ": "포",
	"ま": "마", "み": "미", "む": "무", "め": "메", "も": "모",
	"や": "야", "ゆ": "유", "よ": "요",
	"ら": "라", "り": "리", "る": "루", "레": "레", "ろ": "로",
	"わ": "와", "を": "오", "ん": "응",
	"ぁ": "아", "ぃ": "이", "ぅ": "우", "ぇ": "에", "ぉ": "오",
	"ゃ": "야", "ゅ": "유", "ょ": "요", "ゎ": "와",
}

func katakanaToHiragana(kata string) string {
	var result strings.Builder
	for _, r := range kata {
		if r >= 0x30A1 && r <= 0x30F6 {
			result.WriteRune(r - 0x60)
		} else if r == 'ー' {
			result.WriteRune('ー')
		} else if r == 'ッ' {
			result.WriteRune('っ')
		} else {
			result.WriteRune(r)
		}
	}
	return result.String()
}

func isJapanese(text string) bool {
	for _, r := range text {
		if (r >= 0x3040 && r <= 0x309F) || (r >= 0x30A0 && r <= 0x30FF) || (r >= 0x4E00 && r <= 0x9FFF) {
			return true
		}
	}
	return false
}

func generateJapanesePhonetic(lines []LyricLine) []string {
	res := make([]string, len(lines))
	for i, l := range lines {
		if !isJapanese(l.Text) {
			continue
		}
		hira := katakanaToHiragana(l.Text)
		var ko strings.Builder
		for _, r := range hira {
			ch := string(r)
			if k, ok := hiraToKo[ch]; ok {
				ko.WriteString(k)
			} else {
				ko.WriteRune(r)
			}
		}
		res[i] = ko.String()
	}
	return res
}

func translateWithGoogle(lines []LyricLine) (string, []string) {
	if len(lines) == 0 {
		return "unknown", nil
	}

	client := http.Client{Timeout: 8 * time.Second}
	detectedLang := "unknown"
	translations := make([]string, len(lines))

	chunkSize := 25
	for start := 0; start < len(lines); start += chunkSize {
		end := start + chunkSize
		if end > len(lines) {
			end = len(lines)
		}

		var chunkTexts []string
		for i := start; i < end; i++ {
			chunkTexts = append(chunkTexts, lines[i].Text)
		}

		combined := strings.Join(chunkTexts, "\n")
		apiURL := "https://translate.googleapis.com/translate_a/single?client=gtx&sl=auto&tl=ko&dt=t&q=" + url.QueryEscape(combined)

		req, err := http.NewRequest("GET", apiURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")

		resp, err := client.Do(req)
		if err != nil {
			slog.Warn("Google translation request failed", "error", err)
			continue
		}

		var rawData []interface{}
		if err := json.NewDecoder(resp.Body).Decode(&rawData); err == nil && len(rawData) > 0 {
			if len(rawData) > 2 {
				if l, ok := rawData[2].(string); ok && l != "" {
					detectedLang = l
				}
			}

			if transList, ok := rawData[0].([]interface{}); ok {
				var fullChunkTrans strings.Builder
				for _, item := range transList {
					if seg, ok := item.([]interface{}); ok && len(seg) > 0 {
						if t, ok := seg[0].(string); ok {
							fullChunkTrans.WriteString(t)
						}
					}
				}
				splitLines := strings.Split(fullChunkTrans.String(), "\n")
				for i := 0; i < (end - start); i++ {
					if i < len(splitLines) {
						translations[start+i] = strings.TrimSpace(splitLines[i])
					}
				}
			}
		}
		resp.Body.Close()
	}

	return detectedLang, translations
}

func parseLyricsAIResponse(rawText string, expectedCount int) (string, []string, []string, error) {
	rawText = strings.TrimSpace(rawText)
	if strings.HasPrefix(rawText, "```json") {
		rawText = strings.TrimPrefix(rawText, "```json")
		rawText = strings.TrimSuffix(rawText, "```")
	} else if strings.HasPrefix(rawText, "```") {
		rawText = strings.TrimPrefix(rawText, "```")
		rawText = strings.TrimSuffix(rawText, "```")
	}
	rawText = strings.TrimSpace(rawText)

	if rawText == "" {
		return "", nil, nil, fmt.Errorf("empty raw text")
	}

	trans := make([]string, expectedCount)
	pron := make([]string, expectedCount)
	detectedLang := "ja"

	var objMap map[string]interface{}
	if err := json.Unmarshal([]byte(rawText), &objMap); err == nil {
		if l, ok := objMap["lang"].(string); ok && l != "" {
			detectedLang = l
		}
		if tList, ok := objMap["translations"].([]interface{}); ok {
			for i, v := range tList {
				if i < expectedCount {
					if s, ok := v.(string); ok {
						trans[i] = s
					}
				}
			}
		}
		if pList, ok := objMap["pronunciations"].([]interface{}); ok {
			for i, v := range pList {
				if i < expectedCount {
					if s, ok := v.(string); ok {
						pron[i] = s
					}
				}
			}
		}
		if linesList, ok := objMap["lines"].([]interface{}); ok {
			for i, item := range linesList {
				if m, ok := item.(map[string]interface{}); ok {
					idx := -1
					if iv, ok := m["index"].(float64); ok {
						idx = int(iv)
					} else if iv, ok := m["index"].(int); ok {
						idx = iv
					}
					if idx < 0 || idx >= expectedCount {
						idx = i
					}
					tVal := ""
					if t, ok := m["translation"].(string); ok && t != "" {
						tVal = t
					} else if t, ok := m["text"].(string); ok && t != "" {
						tVal = t
					} else if t, ok := m["trans"].(string); ok && t != "" {
						tVal = t
					}
					pVal := ""
					if p, ok := m["pronunciation"].(string); ok && p != "" {
						pVal = p
					} else if p, ok := m["pron"].(string); ok && p != "" {
						pVal = p
					}
					if idx >= 0 && idx < expectedCount {
						trans[idx] = tVal
						pron[idx] = pVal
					}
				}
			}
		}
		return detectedLang, trans, pron, nil
	}

	var arrList []interface{}
	if err := json.Unmarshal([]byte(rawText), &arrList); err == nil && len(arrList) > 0 {
		if firstMap, ok := arrList[0].(map[string]interface{}); ok && firstMap["translations"] != nil {
			if l, ok := firstMap["lang"].(string); ok && l != "" {
				detectedLang = l
			}
			if tList, ok := firstMap["translations"].([]interface{}); ok {
				for i, v := range tList {
					if i < expectedCount {
						if s, ok := v.(string); ok {
							trans[i] = s
						}
					}
				}
			}
			if pList, ok := firstMap["pronunciations"].([]interface{}); ok {
				for i, v := range pList {
					if i < expectedCount {
						if s, ok := v.(string); ok {
							pron[i] = s
						}
					}
				}
			}
			return detectedLang, trans, pron, nil
		}

		for i, item := range arrList {
			if m, ok := item.(map[string]interface{}); ok {
				idx := -1
				if iv, ok := m["index"].(float64); ok {
					idx = int(iv)
				} else if iv, ok := m["index"].(int); ok {
					idx = iv
				}
				if idx < 0 || idx >= expectedCount {
					idx = i
				}
				tVal := ""
				if t, ok := m["translation"].(string); ok && t != "" {
					tVal = t
				} else if t, ok := m["text"].(string); ok && t != "" {
					tVal = t
				} else if t, ok := m["trans"].(string); ok && t != "" {
					tVal = t
				}
				pVal := ""
				if p, ok := m["pronunciation"].(string); ok && p != "" {
					pVal = p
				} else if p, ok := m["pron"].(string); ok && p != "" {
					pVal = p
				}
				if idx >= 0 && idx < expectedCount {
					trans[idx] = tVal
					pron[idx] = pVal
				}
			} else if s, ok := item.(string); ok {
				if i < expectedCount {
					trans[i] = s
				}
			}
		}
		return detectedLang, trans, pron, nil
	}

	return "", nil, nil, fmt.Errorf("failed to parse AI response JSON: %s", rawText)
}

func translateWithGemini(lines []LyricLine, trackName, artist string, apiKey string) (string, []string, []string, string, error) {
	if apiKey == "" || len(lines) == 0 {
		return "", nil, nil, "", fmt.Errorf("api key or lines empty")
	}

	type LineEntry struct {
		Index int    `json:"index"`
		Text  string `json:"text"`
	}
	var sample []LineEntry
	for i := 0; i < len(lines); i++ {
		sample = append(sample, LineEntry{Index: i, Text: lines[i].Text})
	}

	sampleBytes, _ := json.Marshal(sample)

	prompt := fmt.Sprintf(`당신은 전문 음악 가사 번역가입니다.
노래 제목: "%s", 아티스트: "%s"

[가사 번역 및 독음 원칙]
1. [정제되고 자연스러운 한국어 번역]:
   - 원문의 본래 의미와 뉘앙스에 충실하면서, 어색한 기계 직역투 없이 단정하고 매끄러운 한국어로 번역하세요.
   - 지나치게 자극적이거나 과장된 속어/자의적 왜곡을 지양하고, 원작의 정서와 메시지를 깔끔하고 자연스럽게 전달하세요.
2. [다국어 혼합 곡 및 외국어/영어 처리 (맥락 기반 지능형 번역)]:
   - 곡 내에 일본어/중국어/영어 등이 섞여 있는 다국어 곡이라도, **스토리·서사·감정 표현이 담긴 문장형 가사는 예외 없이 자연스러운 한국어로 번역**하세요 (예: "Every day I dream of you" -> "매일 난 널 꿈꿔").
   - 단, 곡의 리듬감이나 포인트가 되는 **짧은 시그니처 훅·감탄사·고유명사**(예: "Yeah", "Baby", "Let's go", "MWAH!", "Tic Toc", "Check it out", "아저씨 구문", "레퀴엠")는 원작의 맛을 살려 그대로 두거나 자연스럽게 살리세요.
3. [문장형 가사의 행간 호흡 분할 원칙 (어순 재배치 & 1:1 매핑)]:
   - 한 문장이 여러 줄에 걸쳐 이어지는 경우(어순 차이가 있는 영어 등), **문장 전체의 매끄러운 한국어 의미를 먼저 완성한 후, 원곡의 호흡(구문/쉼표)에 맞추어 각 줄(index)에 자연스럽게 나누어 배분**하세요.
   - 예:
     - [index: 60] "Take in the weight" -> translation: "매 순간이 품은 무게를", pronunciation: "테이크 인 더 웨이트"
     - [index: 61] "each moment bears" -> translation: "온전히 받아들여", pronunciation: "이치 모먼트 베어스"
   - **주의**: 두 줄의 번역을 1줄에 몰아서 적고 다음 줄을 누락시키는 일 없이, 입력된 모든 index(0부터 %d까지)마다 반드시 빠짐없이 번역과 독음을 나누어 담으세요.
4. [100%% 한글 발음 표기]:
   - pronunciations는 알파벳(Romaji/English)을 쓰지 말고, 반드시 한국인이 읽고 따라 부를 수 있는 자연스러운 **한글 음차 표기**로만 작성하세요 (예: "카네오 나라세바 코노 토오리", "에브리 데이 아이 드림 오브 유").
5. 원곡이 100%% 순수 한국어 곡인 경우에만 translations와 pronunciations를 원문 그대로 두세요.

Input JSON lines:
%s

반드시 아래 JSON 스키마 규격으로만 응답하세요:
{
  "lang": "ja", // ISO 639-1 언어 코드 (ja, en, zh 등)
  "lines": [
    {"index": 0, "translation": "자연스러운 한국어 가사 0", "pronunciation": "한글 발음 0"},
    {"index": 1, "translation": "자연스러운 한국어 가사 1", "pronunciation": "한글 발음 1"}
  ]
}
lines 배열 길이는 입력 줄 수(%d줄)와 정확히 일치해야 하며, 각 줄마다 index, translation, pronunciation 3개 필드를 빠짐없이 채워 index 0부터 %d까지 반환하세요.`, trackName, artist, len(lines)-1, string(sampleBytes), len(lines), len(lines)-1)

	models := []string{"gemini-3.5-flash-lite", "gemini-3.1-flash-lite", "gemini-flash-lite-latest", "gemini-3.7-flash", "gemini-3.5-flash"}
	var lastErr error

	for _, model := range models {
		genConfig := map[string]interface{}{
			"responseMimeType": "application/json",
		}
		if !strings.Contains(model, "lite") {
			genConfig["thinkingConfig"] = map[string]interface{}{
				"thinkingBudget": 0,
			}
		}

		payload := map[string]interface{}{
			"contents": []map[string]interface{}{
				{"parts": []map[string]string{{"text": prompt}}},
			},
			"generationConfig": genConfig,
		}

		jsonBytes, err := json.Marshal(payload)
		if err != nil {
			lastErr = err
			continue
		}

		apiURL := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", model, apiKey)
		httpReq, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonBytes))
		if err != nil {
			lastErr = err
			continue
		}
		httpReq.Header.Set("Content-Type", "application/json")

		client := http.Client{Timeout: 25 * time.Second}
		resp, err := client.Do(httpReq)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("gemini model %s returned status %d", model, resp.StatusCode)
			resp.Body.Close()
			continue
		}

		var geminiResp struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Thought bool   `json:"thought"`
						Text    string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil || len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
			resp.Body.Close()
			lastErr = fmt.Errorf("empty gemini candidates")
			continue
		}
		resp.Body.Close()

		var rawText string
		for _, part := range geminiResp.Candidates[0].Content.Parts {
			if !part.Thought || strings.Contains(part.Text, "translations") || strings.Contains(part.Text, "text") {
				rawText = strings.TrimSpace(part.Text)
				if rawText != "" {
					break
				}
			}
		}
		if rawText == "" {
			rawText = strings.TrimSpace(geminiResp.Candidates[0].Content.Parts[0].Text)
		}

		aiLang, trans, pron, err := parseLyricsAIResponse(rawText, len(lines))
		if err != nil {
			lastErr = err
			continue
		}

		slog.Info("Successfully translated lyrics with Gemini Flash", "model", model, "totalLines", len(lines), "lang", aiLang)
		return aiLang, trans, pron, model, nil
	}

	return "", nil, nil, "", lastErr
}

func translateWithGPT(lines []LyricLine, trackName, artist string, apiKey string) (string, []string, []string, string, error) {
	if apiKey == "" || len(lines) == 0 {
		return "", nil, nil, "", fmt.Errorf("api key or lines empty")
	}

	type LineEntry struct {
		Index int    `json:"index"`
		Text  string `json:"text"`
	}
	var sample []LineEntry
	for i := 0; i < len(lines); i++ {
		sample = append(sample, LineEntry{Index: i, Text: lines[i].Text})
	}

	sampleBytes, _ := json.Marshal(sample)

	prompt := fmt.Sprintf(`당신은 전문 음악 가사 번역가입니다.
노래 제목: "%s", 아티스트: "%s"

[가사 번역 및 독음 원칙]
1. [정제되고 자연스러운 한국어 번역]:
   - 원문의 본래 의미와 뉘앙스에 충실하면서, 어색한 기계 직역투 없이 단정하고 매끄러운 한국어로 번역하세요.
   - 지나치게 자극적이거나 과장된 속어/자의적 왜곡을 지양하고, 원작의 정서와 메시지를 깔끔하고 자연스럽게 전달하세요.
2. [다국어 혼합 곡 및 외국어/영어 처리 (맥락 기반 지능형 번역)]:
   - 곡 내에 일본어/중국어/영어 등이 섞여 있는 다국어 곡이라도, **스토리·서사·감정 표현이 담긴 문장형 가사는 예외 없이 자연스러운 한국어로 번역**하세요 (예: "Every day I dream of you" -> "매일 난 널 꿈꿔").
   - 단, 곡의 리듬감이나 포인트가 되는 **짧은 시그니처 훅·감탄사·고유명사**(예: "Yeah", "Baby", "Let's go", "MWAH!", "Tic Toc", "Check it out", "아저씨 구문", "레퀴엠")는 원작의 맛을 살려 그대로 두거나 자연스럽게 살리세요.
3. [문장형 가사의 행간 호흡 분할 원칙 (어순 재배치 & 1:1 매핑)]:
   - 한 문장이 여러 줄에 걸쳐 이어지는 경우(어순 차이가 있는 영어 등), **문장 전체의 매끄러운 한국어 의미를 먼저 완성한 후, 원곡의 호흡(구문/쉼표)에 맞추어 각 줄(index)에 자연스럽게 나누어 배분**하세요.
   - 예:
     - [index: 60] "Take in the weight" -> translation: "매 순간이 품은 무게를", pronunciation: "테이크 인 더 웨이트"
     - [index: 61] "each moment bears" -> translation: "온전히 받아들여", pronunciation: "이치 모먼트 베어스"
   - **주의**: 두 줄의 번역을 1줄에 몰아서 적고 다음 줄을 누락시키는 일 없이, 입력된 모든 index(0부터 %d까지)마다 반드시 빠짐없이 번역과 독음을 나누어 담으세요.
4. [100%% 한글 발음 표기]:
   - pronunciations는 알파벳(Romaji/English)을 쓰지 말고, 반드시 한국인이 읽고 따라 부를 수 있는 자연스러운 **한글 음차 표기**로만 작성하세요 (예: "카네오 나라세바 코노 토오리", "에브리 데이 아이 드림 오브 유").
5. 원곡이 100%% 순수 한국어 곡인 경우에만 translations와 pronunciations를 원문 그대로 두세요.

Input JSON lines:
%s

반드시 아래 JSON 스키마 규격으로만 응답하세요:
{
  "lang": "ja", // ISO 639-1 언어 코드 (ja, en, zh 등)
  "lines": [
    {"index": 0, "translation": "자연스러운 한국어 가사 0", "pronunciation": "한글 발음 0"},
    {"index": 1, "translation": "자연스러운 한국어 가사 1", "pronunciation": "한글 발음 1"}
  ]
}
lines 배열 길이는 입력 줄 수(%d줄)와 정확히 일치해야 하며, 각 줄마다 index, translation, pronunciation 3개 필드를 빠짐없이 채워 index 0부터 %d까지 반환하세요.`, trackName, artist, len(lines)-1, string(sampleBytes), len(lines), len(lines)-1)

	reqBody := map[string]interface{}{
		"model": "gpt-5.6-luna",
		"messages": []map[string]string{
			{"role": "system", "content": "You are an expert lyrical song translator. Output valid JSON only."},
			{"role": "user", "content": prompt},
		},
		"response_format": map[string]string{"type": "json_object"},
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", nil, nil, "", err
	}

	httpReq, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(jsonBytes))
	if err != nil {
		return "", nil, nil, "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	client := http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", nil, nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", nil, nil, "", fmt.Errorf("status code %d", resp.StatusCode)
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil || len(chatResp.Choices) == 0 {
		return "", nil, nil, "", fmt.Errorf("empty chat response")
	}

	content := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	aiLang, trans, pron, err := parseLyricsAIResponse(content, len(lines))
	if err != nil {
		return "", nil, nil, "", err
	}

	slog.Info("Successfully translated lyrics with gpt-5.6-luna (single request)", "totalLines", len(lines), "lang", aiLang)
	return aiLang, trans, pron, "gpt-5.6-luna", nil
}

func attachLyricsTranslation(result *LyricResult, geminiKey, openAIKey string) {
	if result == nil || len(result.Lines) == 0 {
		return
	}

	var aiLang string
	var aiTrans, aiPron []string
	var modelUsed string
	var aiErr error

	if geminiKey != "" {
		aiLang, aiTrans, aiPron, modelUsed, aiErr = translateWithGemini(result.Lines, result.TrackName, result.Artist, geminiKey)
	}
	if (aiErr != nil || len(aiTrans) == 0) && openAIKey != "" {
		aiLang, aiTrans, aiPron, modelUsed, aiErr = translateWithGPT(result.Lines, result.TrackName, result.Artist, openAIKey)
	}

	if aiLang == "ko" {
		result.mu.Lock()
		result.TranslationModel = "원문"
		result.DetectedLang = "KO"
		result.mu.Unlock()
		return
	}

	if aiErr == nil && len(aiTrans) > 0 {
		var missingIndices []int
		for idx, t := range aiTrans {
			if strings.TrimSpace(t) == "" && strings.TrimSpace(result.Lines[idx].Text) != "" {
				missingIndices = append(missingIndices, idx)
			}
		}
		if len(missingIndices) > 0 {
			_, gTrans := translateWithGoogle(result.Lines)
			for _, idx := range missingIndices {
				if idx < len(gTrans) && strings.TrimSpace(gTrans[idx]) != "" {
					aiTrans[idx] = gTrans[idx]
				}
			}
		}

		result.mu.Lock()
		result.TranslatedLines = aiTrans
		result.PronunciationLines = aiPron
		result.TranslationModel = modelUsed
		result.DetectedLang = aiLang
		result.mu.Unlock()
	} else {
		slog.Warn("AI translation failed or incomplete, using Google Translate fallback", "error", aiErr)
		gLang, gTrans := translateWithGoogle(result.Lines)
		if gLang != "ko" && len(gTrans) > 0 {
			result.mu.Lock()
			result.TranslatedLines = gTrans
			result.TranslationModel = "Google 번역"
			result.DetectedLang = gLang
			result.mu.Unlock()
		}
	}
}
