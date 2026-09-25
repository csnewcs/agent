package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type QuizQuestion struct {
	Question    string   `json:"question"`
	Options     []string `json:"options"`
	AnswerIndex int      `json:"answer_index"`
	Explanation string   `json:"explanation"`
}

type ActiveQuizSession struct {
	sync.Mutex
	ID           string
	UserID       string
	UserName     string
	SolverID     string
	SolverName   string
	Topic        string
	Difficulty   string
	Model        string
	Questions    []*QuizQuestion
	CurrentIndex int
	Score        int
	Answered     bool
	History      []bool
	UserChoices  []int
	CreatedAt    time.Time
}

type ActiveQuiz = ActiveQuizSession

var activeQuizzes sync.Map // key: string (quizID), value: *ActiveQuizSession

func cleanExpiredQuizzes() {
	now := time.Now()
	activeQuizzes.Range(func(key, value any) bool {
		q, ok := value.(*ActiveQuizSession)
		if ok && now.Sub(q.CreatedAt) > 2*time.Hour {
			activeQuizzes.Delete(key)
		}
		return true
	})
}

func buildQuizCommand() (BotCommand, error) {
	minCount := float64(1)
	maxCount := float64(10)
	return NewBotCommandBuilder("ai_quiz").
		WithDescription("AI가 생성한 4지선다 퀴즈를 풀고 정답을 맞혀보세요.").
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionString,
			Name:        "topic",
			Description: "퀴즈 주제 (예: 한국사, 파이썬, 우주과학, 축구)",
			Required:    true,
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionString,
			Name:        "difficulty",
			Description: "난이도 선택 (기본값: 보통)",
			Required:    false,
			Choices: []*discordgo.ApplicationCommandOptionChoice{
				{Name: "쉬움 (Easy)", Value: "easy"},
				{Name: "보통 (Normal)", Value: "normal"},
				{Name: "어려움 (Hard)", Value: "hard"},
			},
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionInteger,
			Name:        "count",
			Description: "문제 수 (1~10, 기본값: 1)",
			Required:    false,
			MinValue:    &minCount,
			MaxValue:    maxCount,
		}).
		WithFunction(handleQuizCommand).
		Build()
}

func getDifficultyLabel(diff string) string {
	switch strings.ToLower(diff) {
	case "easy":
		return "쉬움"
	case "hard":
		return "어려움"
	default:
		return "보통"
	}
}

func cleanOptionText(text string, index int) string {
	text = strings.TrimSpace(text)
	prefixes := []string{
		fmt.Sprintf("%d. ", index+1),
		fmt.Sprintf("%d) ", index+1),
		fmt.Sprintf("(%d) ", index+1),
		fmt.Sprintf("%d번: ", index+1),
		fmt.Sprintf("%d번 ", index+1),
		fmt.Sprintf("%c. ", 'A'+index),
		fmt.Sprintf("%c) ", 'A'+index),
		fmt.Sprintf("(%c) ", 'A'+index),
		fmt.Sprintf("%c. ", 'a'+index),
		fmt.Sprintf("%c) ", 'a'+index),
		fmt.Sprintf("(%c) ", 'a'+index),
	}
	for _, p := range prefixes {
		if strings.HasPrefix(text, p) {
			return strings.TrimSpace(strings.TrimPrefix(text, p))
		}
	}
	circleNumbers := []string{"①", "②", "③", "④"}
	if index >= 0 && index < len(circleNumbers) {
		if strings.HasPrefix(text, circleNumbers[index]+" ") {
			return strings.TrimSpace(strings.TrimPrefix(text, circleNumbers[index]+" "))
		}
		if strings.HasPrefix(text, circleNumbers[index]) {
			return strings.TrimSpace(strings.TrimPrefix(text, circleNumbers[index]))
		}
	}
	return text
}

func stripMarkdownFence(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		idx := strings.Index(raw, "\n")
		if idx != -1 {
			raw = raw[idx+1:]
		}
		if endIdx := strings.LastIndex(raw, "```"); endIdx != -1 {
			raw = raw[:endIdx]
		}
		raw = strings.TrimSpace(raw)
	}
	return raw
}

func validateAndCleanQuestion(q *QuizQuestion) (*QuizQuestion, error) {
	q.Question = strings.TrimSpace(q.Question)
	if q.Question == "" {
		return nil, fmt.Errorf("quiz question is empty")
	}
	if len(q.Options) != 4 {
		return nil, fmt.Errorf("quiz must have exactly 4 options, got %d", len(q.Options))
	}
	for i := range q.Options {
		q.Options[i] = cleanOptionText(q.Options[i], i)
		if q.Options[i] == "" {
			return nil, fmt.Errorf("quiz option %d is empty", i+1)
		}
	}
	if q.AnswerIndex < 0 || q.AnswerIndex > 3 {
		return nil, fmt.Errorf("quiz answer_index %d out of bounds (0-3)", q.AnswerIndex)
	}
	return q, nil
}

func parseQuizQuestionsJSON(raw string) ([]*QuizQuestion, error) {
	raw = stripMarkdownFence(raw)

	tryParse := func(b []byte) []*QuizQuestion {
		// 1. Try wrapped format {"questions": [...]}
		var wrapper struct {
			Questions []QuizQuestion `json:"questions"`
		}
		if err := json.Unmarshal(b, &wrapper); err == nil && len(wrapper.Questions) > 0 {
			var res []*QuizQuestion
			for _, q := range wrapper.Questions {
				if cleaned, err := validateAndCleanQuestion(&q); err == nil {
					res = append(res, cleaned)
				}
			}
			if len(res) > 0 {
				return res
			}
		}

		// 2. Try array format [{...}, {...}]
		var list []QuizQuestion
		if err := json.Unmarshal(b, &list); err == nil && len(list) > 0 {
			var res []*QuizQuestion
			for _, q := range list {
				if cleaned, err := validateAndCleanQuestion(&q); err == nil {
					res = append(res, cleaned)
				}
			}
			if len(res) > 0 {
				return res
			}
		}

		// 3. Try single question format {"question": ...}
		var single QuizQuestion
		if err := json.Unmarshal(b, &single); err == nil {
			if cleaned, err := validateAndCleanQuestion(&single); err == nil {
				return []*QuizQuestion{cleaned}
			}
		}

		return nil
	}

	if qs := tryParse([]byte(raw)); len(qs) > 0 {
		return qs, nil
	}

	startBrace := strings.Index(raw, "{")
	endBrace := strings.LastIndex(raw, "}")
	if startBrace != -1 && endBrace != -1 && endBrace > startBrace {
		if qs := tryParse([]byte(raw[startBrace : endBrace+1])); len(qs) > 0 {
			return qs, nil
		}
	}

	startBracket := strings.Index(raw, "[")
	endBracket := strings.LastIndex(raw, "]")
	if startBracket != -1 && endBracket != -1 && endBracket > startBracket {
		if qs := tryParse([]byte(raw[startBracket : endBracket+1])); len(qs) > 0 {
			return qs, nil
		}
	}

	return nil, fmt.Errorf("failed to parse quiz questions from response: %s", raw)
}

func parseQuizJSON(raw string) (*QuizQuestion, error) {
	qs, err := parseQuizQuestionsJSON(raw)
	if err != nil {
		return nil, err
	}
	if len(qs) == 0 {
		return nil, fmt.Errorf("no quiz questions found in response")
	}
	return qs[0], nil
}

func shuffleQuizOptions(q *QuizQuestion) {
	if q == nil || len(q.Options) != 4 || q.AnswerIndex < 0 || q.AnswerIndex >= len(q.Options) {
		return
	}
	perm := rand.Perm(4)
	newOptions := make([]string, 4)
	newAnswerIdx := -1
	for newIdx, oldIdx := range perm {
		newOptions[newIdx] = q.Options[oldIdx]
		if oldIdx == q.AnswerIndex {
			newAnswerIdx = newIdx
		}
	}
	q.Options = newOptions
	q.AnswerIndex = newAnswerIdx
}

var universalQuizFocusAngles = []string{
	"핵심 특징 및 고유한 개성",
	"대표적인 일화 및 흥미로운 비하인드 스토리",
	"사람들이 흔히 착각하거나 오해하기 쉬운 상식",
	"주요 사건, 업적 및 상징적인 기록",
	"기원, 유래 및 발전 배경",
	"다른 대상과의 차이점, 관계 및 상호작용",
	"상징적인 디테일, 명칭 및 고유 요소",
}

func buildQuizPrompt(topic, diffName string, count int) string {
	seed := rand.Intn(100000)
	angle := universalQuizFocusAngles[rand.Intn(len(universalQuizFocusAngles))]

	if count <= 1 {
		return fmt.Sprintf(`주제: %s
난이도: %s
출제 시드: %d

위 주제와 난이도에 맞는 흥미롭고 참신한 4지선다형 퀴즈 문제 1개를 출제해줘.
- 항상 똑같이 반복되는 가장 뻔한 대표 질문(단순 클리셰)만을 고집하지 말고, 주제의 다채로운 측면(특징, 유래, 대표 일화, 흔한 오해, 상징적 디테일 등)을 두루 고려하여 출제해줘.
- 참고 관점 힌트: '%s' (주제 성격에 맞게 유연하게 참고하되 억지로 얽매이지 말고 사실에 기반해 출제)
- 보기는 반드시 4개여야 하고, 정답 인덱스(0, 1, 2, 3 중 하나)와 명확한 해설을 포함해야 해.
- 반드시 공식 설정이나 사실에 기반한 정확한 정보로만 출제해야 하며, 불확실하거나 존재하지 않는 허위 사실(할루시네이션)을 절대로 지어내지 마.
- 다른 부가 설명이나 인사말 없이 오직 JSON 형식으로만 응답해줘.
형식:
{
  "questions": [
    {
      "question": "문제 내용",
      "options": [
        "1번 보기",
        "2번 보기",
        "3번 보기",
        "4번 보기"
      ],
      "answer_index": 0,
      "explanation": "해설"
    }
  ]
}`, topic, diffName, seed, angle)
	}

	return fmt.Sprintf(`주제: %s
난이도: %s
문제 수: %d개
출제 시드: %d

위 주제와 난이도에 맞는 서로 다른 흥미롭고 참신한 4지선다형 퀴즈 문제 %d개를 출제해줘.
- %d개 문제가 서로 겹치거나 유사한 내용을 묻지 않도록, 다양한 하위 영역과 다각도의 관점(기본 특징, 유래/배경, 주요 사건/일화, 흔한 오해/상식, 상징적 디테일 등)에서 골고루 출제해줘.
- 각 문제마다 보기는 반드시 4개여야 하고, 정답 인덱스(0, 1, 2, 3 중 하나)와 명확한 해설을 포함해야 해.
- 반드시 공식 설정이나 사실에 기반한 정확한 정보로만 출제해야 하며, 불확실하거나 존재하지 않는 허위 사실(할루시네이션)을 절대로 지어내지 마.
- 다른 부가 설명이나 인사말 없이 오직 JSON 형식으로만 응답해줘.
형식:
{
  "questions": [
    {
      "question": "1번 문제 내용",
      "options": ["1번 보기", "2번 보기", "3번 보기", "4번 보기"],
      "answer_index": 0,
      "explanation": "해설"
    }
  ]
}`, topic, diffName, count, seed, count, count)
}

func generateQuizWithGPT(topic, difficulty string, count int, apiKey string) ([]*QuizQuestion, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("openai api key not configured")
	}

	diffName := getDifficultyLabel(difficulty)
	prompt := buildQuizPrompt(topic, diffName, count)

	payload := map[string]interface{}{
		"model": "gpt-5.6-terra",
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"seed": rand.Intn(1000000),
		"response_format": map[string]string{
			"type": "json_object",
		},
	}

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(jsonBytes))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	timeoutSec := 45
	if count > 3 {
		timeoutSec = 75
	}
	client := http.Client{Timeout: time.Duration(timeoutSec) * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai returned status %d: %s", resp.StatusCode, string(b))
	}

	var gptResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&gptResp); err != nil {
		return nil, err
	}

	if len(gptResp.Choices) == 0 {
		return nil, fmt.Errorf("empty choices from openai")
	}

	return parseQuizQuestionsJSON(gptResp.Choices[0].Message.Content)
}

func generateQuizWithProxy(topic, difficulty string, count int) ([]*QuizQuestion, error) {
	sessionID := fmt.Sprintf("quiz_%d_%d", time.Now().UnixNano(), rand.Intn(10000))
	turnID := fmt.Sprintf("turn_%d", time.Now().UnixNano())
	defer ClearAntigravityProxySession(sessionID)

	diffName := getDifficultyLabel(difficulty)
	prompt := buildQuizPrompt(topic, diffName, count)

	streamChan, cancel, err := SendAntigravityChatStream(sessionID, "default", prompt, turnID, "gemini-3.8-flash-high", nil)
	if err != nil {
		return nil, err
	}
	defer cancel()

	var fullText strings.Builder
	timeout := time.After(45 * time.Second)

	for {
		select {
		case <-timeout:
			return nil, fmt.Errorf("quiz generation timed out")
		case chunk, ok := <-streamChan:
			if !ok {
				goto PARSE
			}
			if chunk.Type == "chat" {
				fullText.WriteString(chunk.Output)
			} else if chunk.Type == "completed" {
				goto PARSE
			} else if chunk.Type == "error" {
				return nil, fmt.Errorf("proxy error: %s", chunk.Output)
			}
		}
	}

PARSE:
	return parseQuizQuestionsJSON(fullText.String())
}

func generateQuizWithGemini(topic, difficulty string, count int, apiKey string) ([]*QuizQuestion, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("gemini api key not configured")
	}

	diffName := getDifficultyLabel(difficulty)
	prompt := buildQuizPrompt(topic, diffName, count)

	models := []string{"gemini-3.5-flash-lite", "gemini-2.5-flash", "gemini-2.0-flash", "gemini-1.5-flash"}
	for _, model := range models {
		payload := map[string]interface{}{
			"contents": []map[string]interface{}{
				{"parts": []map[string]string{{"text": prompt}}},
			},
			"generationConfig": map[string]interface{}{
				"responseMimeType": "application/json",
				"temperature":      1.15,
			},
		}
		jsonBytes, err := json.Marshal(payload)
		if err != nil {
			continue
		}

		apiURL := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", model, apiKey)
		httpReq, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonBytes))
		if err != nil {
			continue
		}
		httpReq.Header.Set("Content-Type", "application/json")

		client := http.Client{Timeout: 35 * time.Second}
		resp, err := client.Do(httpReq)
		if err != nil {
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			continue
		}

		var geminiResp struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&geminiResp)
		resp.Body.Close()

		if decodeErr == nil && len(geminiResp.Candidates) > 0 && len(geminiResp.Candidates[0].Content.Parts) > 0 {
			questions, parseErr := parseQuizQuestionsJSON(geminiResp.Candidates[0].Content.Parts[0].Text)
			if parseErr == nil && len(questions) > 0 {
				return questions, nil
			}
		}
	}

	return nil, fmt.Errorf("gemini fallback failed")
}

func generateQuiz(topic, difficulty string, count int) ([]*QuizQuestion, string, error) {
	var questions []*QuizQuestion
	var modelName string

	// 1. Primary: GPT-5.6 Terra (precise difficulty calibration, structured JSON)
	if botConfig != nil && botConfig.OpenAIAPIKey != "" {
		qGPT, errGPT := generateQuizWithGPT(topic, difficulty, count, botConfig.OpenAIAPIKey)
		if errGPT == nil && len(qGPT) > 0 {
			slog.Info("Generated quiz with GPT-5.6 Terra", "topic", topic, "difficulty", difficulty, "count", len(qGPT))
			questions = qGPT
			modelName = "GPT-5.6 Terra"
		} else {
			slog.Warn("GPT-5.6 Terra quiz generation failed, falling back to direct Gemini API", "error", errGPT)
		}
	}

	// 2. Fast Fallback 1: Gemini direct API (fast 1~2s fallback, avoiding 35s CLI proxy stall)
	if len(questions) == 0 && botConfig != nil && botConfig.GeminiAPIKey != "" {
		qGemini, errGemini := generateQuizWithGemini(topic, difficulty, count, botConfig.GeminiAPIKey)
		if errGemini == nil && len(qGemini) > 0 {
			slog.Info("Generated quiz with Gemini API fallback", "topic", topic, "difficulty", difficulty, "count", len(qGemini))
			questions = qGemini
			modelName = "Gemini Flash API"
		} else {
			slog.Warn("Gemini API quiz fallback failed, trying Antigravity Proxy", "error", errGemini)
		}
	}

	// 3. Fallback 2: Antigravity Proxy (last resort)
	if len(questions) == 0 {
		qProxy, errProxy := generateQuizWithProxy(topic, difficulty, count)
		if errProxy == nil && len(qProxy) > 0 {
			slog.Info("Generated quiz with Antigravity Proxy", "topic", topic, "difficulty", difficulty, "count", len(qProxy))
			questions = qProxy
			modelName = "Gemini 3.8 Flash (Proxy)"
		} else {
			slog.Warn("Proxy quiz generation failed", "error", errProxy)
		}
	}

	if len(questions) == 0 {
		return nil, "", fmt.Errorf("failed to generate quiz across all providers")
	}

	// Shuffle options randomly to eliminate position bias
	for _, q := range questions {
		shuffleQuizOptions(q)
	}

	return questions, modelName, nil
}

func buildQuestionComponents(sess *ActiveQuizSession) []discordgo.MessageComponent {
	currQ := sess.Questions[sess.CurrentIndex]
	total := len(sess.Questions)

	var title string
	if total > 1 {
		title = fmt.Sprintf("AI 퀴즈: %s (%d/%d)", sess.Topic, sess.CurrentIndex+1, total)
	} else {
		title = fmt.Sprintf("AI 퀴즈: %s", sess.Topic)
	}

	var subTitle string
	if total > 1 && sess.CurrentIndex > 0 {
		subTitle = fmt.Sprintf("난이도: %s • 현재 점수: %d/%d", sess.Difficulty, sess.Score, sess.CurrentIndex)
	} else {
		subTitle = fmt.Sprintf("난이도: %s", sess.Difficulty)
	}

	bodyLines := []string{
		fmt.Sprintf("**Q. %s**", currQ.Question),
		"",
		fmt.Sprintf("1. %s", currQ.Options[0]),
		fmt.Sprintf("2. %s", currQ.Options[1]),
		fmt.Sprintf("3. %s", currQ.Options[2]),
		fmt.Sprintf("4. %s", currQ.Options[3]),
	}

	buttons := []discordgo.MessageComponent{
		discordgo.Button{
			Label:    "1번",
			CustomID: fmt.Sprintf("quiz_ans:%s:0", sess.ID),
			Style:    discordgo.SecondaryButton,
		},
		discordgo.Button{
			Label:    "2번",
			CustomID: fmt.Sprintf("quiz_ans:%s:1", sess.ID),
			Style:    discordgo.SecondaryButton,
		},
		discordgo.Button{
			Label:    "3번",
			CustomID: fmt.Sprintf("quiz_ans:%s:2", sess.ID),
			Style:    discordgo.SecondaryButton,
		},
		discordgo.Button{
			Label:    "4번",
			CustomID: fmt.Sprintf("quiz_ans:%s:3", sess.ID),
			Style:    discordgo.SecondaryButton,
		},
	}

	var footer string
	if sess.SolverName != "" {
		footer = fmt.Sprintf("출제 모델: %s • 풀이자: %s • 정답을 선택하세요.", sess.Model, sess.SolverName)
	} else {
		footer = fmt.Sprintf("출제 모델: %s • 4개 보기 중 정답을 선택하세요. (누구나 참여 가능)", sess.Model)
	}

	return NewComponentsBuilder().
		WithTitle(title).
		WithSubTitle(subTitle).
		WithBody(strings.Join(bodyLines, "\n")).
		WithFooter(footer).
		WithButtons(buttons...).
		Build()
}

func buildAnsweredComponents(sess *ActiveQuizSession, chosenIndex int, solverName string) []discordgo.MessageComponent {
	currQ := sess.Questions[sess.CurrentIndex]
	total := len(sess.Questions)
	isCorrect := (chosenIndex == currQ.AnswerIndex)

	var title string
	if total > 1 {
		if isCorrect {
			title = fmt.Sprintf("AI 퀴즈 (%d/%d): 정답입니다!", sess.CurrentIndex+1, total)
		} else {
			title = fmt.Sprintf("AI 퀴즈 (%d/%d): 오답입니다.", sess.CurrentIndex+1, total)
		}
	} else {
		if isCorrect {
			title = "AI 퀴즈: 정답입니다!"
		} else {
			title = "AI 퀴즈: 오답입니다."
		}
	}

	var subTitle string
	if isCorrect {
		subTitle = "축하합니다! 정답을 맞히셨습니다."
	} else {
		if total > 1 && sess.CurrentIndex < total-1 {
			subTitle = "아쉽네요! 다음 문제에 도전해보세요."
		} else {
			subTitle = "아쉽네요! 다음 기회에 다시 도전해보세요."
		}
	}

	var chosenText string
	if chosenIndex >= 0 && chosenIndex < 4 {
		chosenText = fmt.Sprintf("%d번 (%s)", chosenIndex+1, currQ.Options[chosenIndex])
	} else {
		chosenText = "선택 없음"
	}
	correctText := fmt.Sprintf("%d번 (%s)", currQ.AnswerIndex+1, currQ.Options[currQ.AnswerIndex])

	resultTag := "(오답)"
	if isCorrect {
		resultTag = "(정답)"
	}

	bodyLines := []string{
		fmt.Sprintf("**Q. %s**", currQ.Question),
		"",
		fmt.Sprintf("1. %s", currQ.Options[0]),
		fmt.Sprintf("2. %s", currQ.Options[1]),
		fmt.Sprintf("3. %s", currQ.Options[2]),
		fmt.Sprintf("4. %s", currQ.Options[3]),
		"",
		fmt.Sprintf("선택한 답: **%s** %s", chosenText, resultTag),
		"",
		fmt.Sprintf("정답: **%s**", correctText),
	}

	// Option buttons (disabled)
	var optionButtons []discordgo.MessageComponent
	for i := 0; i < 4; i++ {
		label := fmt.Sprintf("%d번", i+1)
		style := discordgo.SecondaryButton

		if i == currQ.AnswerIndex {
			style = discordgo.SuccessButton
			label = fmt.Sprintf("%d번 (정답)", i+1)
		} else if !isCorrect && i == chosenIndex {
			style = discordgo.DangerButton
			label = fmt.Sprintf("%d번 (선택)", i+1)
		}

		optionButtons = append(optionButtons, discordgo.Button{
			Label:    label,
			CustomID: fmt.Sprintf("quiz_done:%s:%d:%d", sess.ID, sess.CurrentIndex, i),
			Style:    style,
			Disabled: true,
		})
	}

	footerStatus := "오답"
	if isCorrect {
		footerStatus = "정답"
	}

	var footer string
	if total > 1 {
		footer = fmt.Sprintf("주제: %s • 난이도: %s • 출제자: %s • 풀이자: %s (%s) • 점수: %d/%d", sess.Topic, sess.Difficulty, sess.Model, solverName, footerStatus, sess.Score, sess.CurrentIndex+1)
	} else {
		footer = fmt.Sprintf("주제: %s • 난이도: %s • 출제자: %s • 풀이자: %s (%s)", sess.Topic, sess.Difficulty, sess.Model, solverName, footerStatus)
	}

	dividerTrue := true
	smallSpacing := discordgo.SeparatorSpacingSizeSmall

	builder := NewComponentsBuilder().
		WithTitle(title).
		WithSubTitle(subTitle).
		WithBody(strings.Join(bodyLines, "\n"))

	if currQ.Explanation != "" {
		builder.AddRaw(discordgo.Separator{
			Divider: &dividerTrue,
			Spacing: &smallSpacing,
		})
		builder.AddRaw(discordgo.TextDisplay{
			Content: fmt.Sprintf("**해설**\n%s", currQ.Explanation),
		})
	}

	// If this was the last question of a multi-question quiz, append session summary!
	if total > 1 && sess.CurrentIndex == total-1 {
		pct := int(float64(sess.Score) / float64(total) * 100)
		builder.AddRaw(discordgo.Separator{
			Divider: &dividerTrue,
			Spacing: &smallSpacing,
		})
		var summaryLines []string
		summaryLines = append(summaryLines, fmt.Sprintf("### 🏆 퀴즈 세션 완료! (총 %d문제 중 %d문제 정답 • %d%%)", total, sess.Score, pct))
		for idx, h := range sess.History {
			mark := "❌ 오답"
			if h {
				mark = "⭕ 정답"
			}
			summaryLines = append(summaryLines, fmt.Sprintf("- **%d번 문제**: %s", idx+1, mark))
		}
		builder.AddRaw(discordgo.TextDisplay{
			Content: strings.Join(summaryLines, "\n"),
		})
	}

	builder.WithFooter(footer)

	// Add option buttons (Row 1)
	builder.WithButtons(optionButtons...)

	// If more questions remaining, add Next Question button (Row 2)!
	if total > 1 && sess.CurrentIndex < total-1 {
		builder.AddRaw(discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    fmt.Sprintf("다음 문제 (%d/%d) ▶", sess.CurrentIndex+2, total),
					CustomID: fmt.Sprintf("quiz_next:%s", sess.ID),
					Style:    discordgo.PrimaryButton,
				},
			},
		})
	} else if sess.CurrentIndex == total-1 {
		downloadLabel := "📥 전체 문제/해설 다운로드 (.txt)"
		if total == 1 {
			downloadLabel = "📥 문제/해설 다운로드 (.txt)"
		}
		builder.AddRaw(discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    downloadLabel,
					CustomID: fmt.Sprintf("quiz_download:%s", sess.ID),
					Style:    discordgo.SuccessButton,
				},
			},
		})
	}

	return builder.Build()
}

func handleQuizCommand(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	var topic string
	difficulty := "normal"
	count := 1

	options := ic.ApplicationCommandData().Options
	for _, opt := range options {
		switch opt.Name {
		case "topic":
			topic = strings.TrimSpace(opt.StringValue())
		case "difficulty":
			difficulty = strings.TrimSpace(opt.StringValue())
		case "count":
			count = int(opt.IntValue())
		}
	}

	if count < 1 {
		count = 1
	}
	if count > 10 {
		count = 10
	}

	if topic == "" {
		_ = RespondComponentsV2(s, ic, SimpleErrorCard("퀴즈 주제를 입력해주세요."), true)
		return
	}

	// Defer response to allow AI time to generate
	err := s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	if err != nil {
		slog.Error("Failed to defer interaction for /ai_quiz", "error", err)
		return
	}

	go func() {
		cleanExpiredQuizzes()

		questions, modelName, err := generateQuiz(topic, difficulty, count)
		if err != nil || len(questions) == 0 {
			slog.Error("Failed to generate quiz", "topic", topic, "difficulty", difficulty, "count", count, "error", err)
			_, _ = EditInteractionComponentsV2(s, ic, SimpleErrorCard("퀴즈를 생성하는데 실패했습니다. 잠시 후 다시 시도해주세요."))
			return
		}

		quizID := fmt.Sprintf("quiz_%d_%d", time.Now().UnixNano(), rand.Intn(10000))
		userName := getUserName(ic)
		diffLabel := getDifficultyLabel(difficulty)

		session := &ActiveQuizSession{
			ID:           quizID,
			UserID:       getUserID(ic),
			UserName:     userName,
			Topic:        topic,
			Difficulty:   diffLabel,
			Model:        modelName,
			Questions:    questions,
			CurrentIndex: 0,
			Score:        0,
			Answered:     false,
			CreatedAt:    time.Now(),
		}
		activeQuizzes.Store(quizID, session)

		comps := buildQuestionComponents(session)

		_, editErr := EditInteractionComponentsV2(s, ic, comps)
		if editErr != nil {
			slog.Error("Failed to edit interaction response for /ai_quiz", "error", editErr)
		}
	}()
}

func handleQuizComponent(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	customID := ic.MessageComponentData().CustomID

	if strings.HasPrefix(customID, "quiz_ans:") {
		handleQuizAnswer(s, ic)
		return
	}
	if strings.HasPrefix(customID, "quiz_next:") {
		handleQuizNext(s, ic)
		return
	}
	if strings.HasPrefix(customID, "quiz_download:") {
		handleQuizDownload(s, ic)
		return
	}
}

func handleQuizAnswer(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	customID := ic.MessageComponentData().CustomID
	parts := strings.Split(customID, ":")
	if len(parts) != 3 {
		return
	}
	sessionID := parts[1]
	choiceIdx, err := strconv.Atoi(parts[2])
	if err != nil || choiceIdx < 0 || choiceIdx > 3 {
		return
	}

	val, ok := activeQuizzes.Load(sessionID)
	if !ok {
		_ = RespondComponentsV2(s, ic, SimpleErrorCard("만료되었거나 유효하지 않은 퀴즈입니다."), true)
		return
	}

	sess, ok := val.(*ActiveQuizSession)
	if !ok {
		_ = RespondComponentsV2(s, ic, SimpleErrorCard("퀴즈 데이터를 불러올 수 없습니다."), true)
		return
	}

	callerID := getUserID(ic)
	callerName := getUserName(ic)

	sess.Lock()
	if sess.SolverID != "" && sess.SolverID != callerID {
		lockedSolver := sess.SolverName
		sess.Unlock()
		_ = RespondComponentsV2(s, ic, SimpleErrorCard(fmt.Sprintf("이 퀴즈는 **%s**님이 이미 풀이 중입니다.", lockedSolver)), true)
		return
	}

	if sess.Answered || sess.CurrentIndex >= len(sess.Questions) {
		sess.Unlock()
		_ = RespondComponentsV2(s, ic, SimpleErrorCard("이미 풀이가 완료된 문제입니다."), true)
		return
	}

	// First interaction locks the solver
	if sess.SolverID == "" {
		sess.SolverID = callerID
		sess.SolverName = callerName
	}

	sess.Answered = true
	currQ := sess.Questions[sess.CurrentIndex]
	isCorrect := (choiceIdx == currQ.AnswerIndex)
	if isCorrect {
		sess.Score++
	}
	sess.History = append(sess.History, isCorrect)
	sess.UserChoices = append(sess.UserChoices, choiceIdx)
	solverName := sess.SolverName
	sess.Unlock()

	comps := buildAnsweredComponents(sess, choiceIdx, solverName)

	_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Components: comps,
			Flags:      discordgo.MessageFlagsIsComponentsV2,
		},
	})
}

func handleQuizNext(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	customID := ic.MessageComponentData().CustomID
	parts := strings.Split(customID, ":")
	if len(parts) != 2 {
		return
	}
	sessionID := parts[1]

	val, ok := activeQuizzes.Load(sessionID)
	if !ok {
		_ = RespondComponentsV2(s, ic, SimpleErrorCard("만료되었거나 유효하지 않은 퀴즈입니다."), true)
		return
	}

	sess, ok := val.(*ActiveQuizSession)
	if !ok {
		_ = RespondComponentsV2(s, ic, SimpleErrorCard("퀴즈 데이터를 불러올 수 없습니다."), true)
		return
	}

	callerID := getUserID(ic)

	sess.Lock()
	if sess.SolverID != "" && sess.SolverID != callerID {
		lockedSolver := sess.SolverName
		sess.Unlock()
		_ = RespondComponentsV2(s, ic, SimpleErrorCard(fmt.Sprintf("이 퀴즈는 **%s**님이 풀이 중입니다.", lockedSolver)), true)
		return
	}

	if !sess.Answered {
		sess.Unlock()
		_ = RespondComponentsV2(s, ic, SimpleErrorCard("현재 문제를 먼저 풀어주세요."), true)
		return
	}
	if sess.CurrentIndex >= len(sess.Questions)-1 {
		sess.Unlock()
		_ = RespondComponentsV2(s, ic, SimpleErrorCard("이미 모든 문제를 풀었습니다."), true)
		return
	}
	sess.CurrentIndex++
	sess.Answered = false
	sess.Unlock()

	comps := buildQuestionComponents(sess)

	_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Components: comps,
			Flags:      discordgo.MessageFlagsIsComponentsV2,
		},
	})
}

func handleQuizDownload(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	customID := ic.MessageComponentData().CustomID
	parts := strings.Split(customID, ":")
	if len(parts) != 2 {
		return
	}
	sessionID := parts[1]

	val, ok := activeQuizzes.Load(sessionID)
	if !ok {
		_ = RespondComponentsV2(s, ic, SimpleErrorCard("만료되었거나 유효하지 않은 퀴즈 세션입니다."), true)
		return
	}

	sess, ok := val.(*ActiveQuizSession)
	if !ok {
		_ = RespondComponentsV2(s, ic, SimpleErrorCard("퀴즈 데이터를 불러올 수 없습니다."), true)
		return
	}

	sess.Lock()
	solver := sess.SolverName
	if solver == "" {
		solver = sess.UserName
	}
	sess.Unlock()

	reviewText := generateQuizReviewText(sess)
	cleanTopic := sanitizeQuizFileName(sess.Topic)
	fileName := fmt.Sprintf("quiz_review_%s.txt", cleanTopic)

	content := fmt.Sprintf("📄 **%s** 퀴즈 전체 문제 및 정답·해설 파일입니다.", sess.Topic)
	if solver != "" {
		content = fmt.Sprintf("📄 **%s** 퀴즈 전체 문제 및 정답·해설 파일입니다. (풀이자: %s, 점수: %d/%d)", sess.Topic, solver, sess.Score, len(sess.Questions))
	}

	err := s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Files: []*discordgo.File{
				{
					Name:        fileName,
					ContentType: "text/plain; charset=utf-8",
					Reader:      strings.NewReader(reviewText),
				},
			},
		},
	})
	if err != nil {
		slog.Error("Failed to send quiz review file", "error", err)
	}
}

func sanitizeQuizFileName(name string) string {
	reg := regexp.MustCompile(`[^\w가-힣\-_]+`)
	cleaned := reg.ReplaceAllString(strings.TrimSpace(name), "_")
	cleaned = strings.Trim(cleaned, "_")
	if cleaned == "" {
		return "quiz"
	}
	runes := []rune(cleaned)
	if len(runes) > 30 {
		cleaned = string(runes[:30])
	}
	return cleaned
}

func generateQuizReviewText(sess *ActiveQuizSession) string {
	sess.Lock()
	defer sess.Unlock()

	var sb strings.Builder
	total := len(sess.Questions)
	pct := 0
	if total > 0 {
		pct = int(float64(sess.Score) / float64(total) * 100)
	}

	solver := sess.SolverName
	if solver == "" {
		solver = sess.UserName
	}

	sb.WriteString("============================================================\n")
	sb.WriteString(fmt.Sprintf("[AI QUIZ REVIEW] %s\n", sess.Topic))
	sb.WriteString(fmt.Sprintf("- 난이도: %s | 출제 모델: %s\n", sess.Difficulty, sess.Model))
	if solver != "" {
		sb.WriteString(fmt.Sprintf("- 풀이자: %s | 최종 점수: %d / %d (%d%%)\n", solver, sess.Score, total, pct))
	} else {
		sb.WriteString(fmt.Sprintf("- 최종 점수: %d / %d (%d%%)\n", sess.Score, total, pct))
	}
	if sess.UserName != "" && sess.UserName != solver {
		sb.WriteString(fmt.Sprintf("- 출제자: %s\n", sess.UserName))
	}
	sb.WriteString(fmt.Sprintf("- 출제 일시: %s\n", sess.CreatedAt.Format("2006-01-02 15:04:05")))
	sb.WriteString("============================================================\n\n")

	for i, q := range sess.Questions {
		sb.WriteString(fmt.Sprintf("[문제 %d] %s\n", i+1, q.Question))
		for optIdx, opt := range q.Options {
			sb.WriteString(fmt.Sprintf("  %d. %s\n", optIdx+1, opt))
		}
		sb.WriteString("\n")

		var userChoice = -1
		if i < len(sess.UserChoices) {
			userChoice = sess.UserChoices[i]
		}

		if userChoice >= 0 && userChoice < len(q.Options) {
			userChoiceText := q.Options[userChoice]
			if userChoice == q.AnswerIndex {
				sb.WriteString(fmt.Sprintf("* 제출한 답: %d번 (%s) [⭕ 정답]\n", userChoice+1, userChoiceText))
			} else {
				sb.WriteString(fmt.Sprintf("* 제출한 답: %d번 (%s) [❌ 오답]\n", userChoice+1, userChoiceText))
			}
		}

		if q.AnswerIndex >= 0 && q.AnswerIndex < len(q.Options) {
			sb.WriteString(fmt.Sprintf("* 실제 정답: %d번 (%s)\n", q.AnswerIndex+1, q.Options[q.AnswerIndex]))
		}

		if q.Explanation != "" {
			sb.WriteString(fmt.Sprintf("* 해설: %s\n", q.Explanation))
		}

		if i < total-1 {
			sb.WriteString("\n------------------------------------------------------------\n\n")
		} else {
			sb.WriteString("\n============================================================\n")
		}
	}

	return sb.String()
}
