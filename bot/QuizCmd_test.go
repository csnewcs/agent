package main

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestParseQuizJSON(t *testing.T) {
	// Case 1: Clean raw JSON
	raw1 := `{
		"question": "파이썬에서 가변(mutable) 객체는 무엇인가요?",
		"options": ["int", "str", "tuple", "list"],
		"answer_index": 3,
		"explanation": "list는 가변 객체입니다."
	}`
	q1, err := parseQuizJSON(raw1)
	if err != nil {
		t.Fatalf("Failed to parse clean JSON: %v", err)
	}
	if q1.Question != "파이썬에서 가변(mutable) 객체는 무엇인가요?" {
		t.Errorf("Unexpected question: %s", q1.Question)
	}
	if len(q1.Options) != 4 || q1.Options[3] != "list" {
		t.Errorf("Unexpected options: %v", q1.Options)
	}
	if q1.AnswerIndex != 3 {
		t.Errorf("Unexpected answer_index: %d", q1.AnswerIndex)
	}

	// Case 2: Markdown code fence with prefix numbers in options
	raw2 := "```json\n" + `{
		"question": "대한민국의 수도는?",
		"options": [
			"1. 부산",
			"2. 대구",
			"3. 서울",
			"4. 인천"
		],
		"answer_index": 2,
		"explanation": "서울입니다."
	}` + "\n```"
	q2, err := parseQuizJSON(raw2)
	if err != nil {
		t.Fatalf("Failed to parse markdown JSON: %v", err)
	}
	if q2.Options[0] != "부산" || q2.Options[2] != "서울" {
		t.Errorf("Failed to clean option prefixes: %v", q2.Options)
	}
	if q2.AnswerIndex != 2 {
		t.Errorf("Unexpected answer_index: %d", q2.AnswerIndex)
	}

	// Case 3: Invalid answer index out of bounds
	raw3 := `{
		"question": "문제",
		"options": ["A", "B", "C", "D"],
		"answer_index": 5,
		"explanation": "해설"
	}`
	_, err3 := parseQuizJSON(raw3)
	if err3 == nil {
		t.Errorf("Expected error for out of bounds answer_index, got nil")
	}

	// Case 4: Less than 4 options
	raw4 := `{
		"question": "문제",
		"options": ["A", "B"],
		"answer_index": 0,
		"explanation": "해설"
	}`
	_, err4 := parseQuizJSON(raw4)
	if err4 == nil {
		t.Errorf("Expected error for insufficient options, got nil")
	}
}

func TestCleanOptionText(t *testing.T) {
	tests := []struct {
		input    string
		index    int
		expected string
	}{
		{"1. 사과", 0, "사과"},
		{"2) 바나나", 1, "바나나"},
		{"(3) 포도", 2, "포도"},
		{"4번: 딸기", 3, "딸기"},
		{"① 복숭아", 0, "복숭아"},
		{"② 배", 1, "배"},
		{"③ 감", 2, "감"},
		{"④ 수박", 3, "수박"},
		{"A. 망고", 0, "망고"},
		{"B) 오렌지", 1, "오렌지"},
		{"그냥 과일", 0, "그냥 과일"},
	}

	for _, tc := range tests {
		result := cleanOptionText(tc.input, tc.index)
		if result != tc.expected {
			t.Errorf("cleanOptionText(%q, %d) = %q; want %q", tc.input, tc.index, result, tc.expected)
		}
	}
}

func TestGetDifficultyLabel(t *testing.T) {
	if getDifficultyLabel("easy") != "쉬움" {
		t.Errorf("Expected 쉬움, got %s", getDifficultyLabel("easy"))
	}
	if getDifficultyLabel("normal") != "보통" {
		t.Errorf("Expected 보통, got %s", getDifficultyLabel("normal"))
	}
	if getDifficultyLabel("hard") != "어려움" {
		t.Errorf("Expected 어려움, got %s", getDifficultyLabel("hard"))
	}
	if getDifficultyLabel("unknown") != "보통" {
		t.Errorf("Expected fallback 보통, got %s", getDifficultyLabel("unknown"))
	}
}

func TestQuizSolvedComponentsLayout(t *testing.T) {
	dividerTrue := true
	smallSpacing := discordgo.SeparatorSpacingSizeSmall

	body := "**Q. 질문**\n\n1. 보기1\n2. 보기2\n3. 보기3\n4. 보기4\n\n선택한 답: **1번 (보기1)** (오답)\n\n정답: **2번 (보기2)**"
	explanation := "보기2가 정답인 이유는..."

	builder := NewComponentsBuilder().
		WithTitle("AI 퀴즈: 오답입니다.").
		WithSubTitle("아쉽네요! 다음 기회에 다시 도전해보세요.").
		WithBody(body)

	builder.AddRaw(discordgo.Separator{
		Divider: &dividerTrue,
		Spacing: &smallSpacing,
	})
	builder.AddRaw(discordgo.TextDisplay{
		Content: "**해설**\n" + explanation,
	})

	comps := builder.
		WithFooter("주제: 테스트 • 난이도: 보통 • 풀이자: 테스트유저 (오답)").
		WithButtons(discordgo.Button{
			Label:    "1번 (선택)",
			CustomID: "btn_1",
			Style:    discordgo.DangerButton,
			Disabled: true,
		}).
		Build()

	// Verification of components order:
	// 0: TextDisplay (Title/Subtitle)
	// 1: Separator (Title divider)
	// 2: TextDisplay (Body with question, options, chosen answer, correct answer)
	// 3: Separator (Answer - Explanation divider)
	// 4: TextDisplay (Explanation)
	// 5: Separator (Footer divider)
	// 6: TextDisplay (Footer)
	// 7: ActionsRow (Buttons)
	if len(comps) != 8 {
		t.Fatalf("Expected 8 components in solved layout, got %d", len(comps))
	}

	if _, ok := comps[0].(discordgo.TextDisplay); !ok {
		t.Errorf("Component 0 should be TextDisplay, got %T", comps[0])
	}
	if _, ok := comps[1].(discordgo.Separator); !ok {
		t.Errorf("Component 1 should be Separator, got %T", comps[1])
	}
	if _, ok := comps[2].(discordgo.TextDisplay); !ok {
		t.Errorf("Component 2 should be TextDisplay, got %T", comps[2])
	}
	if sep, ok := comps[3].(discordgo.Separator); !ok || sep.Divider == nil || !*sep.Divider {
		t.Errorf("Component 3 should be Separator with Divider=true, got %v", comps[3])
	}
	if td, ok := comps[4].(discordgo.TextDisplay); !ok || td.Content != "**해설**\n"+explanation {
		t.Errorf("Component 4 should be Explanation TextDisplay, got %v", comps[4])
	}
	if _, ok := comps[5].(discordgo.Separator); !ok {
		t.Errorf("Component 5 should be Separator, got %T", comps[5])
	}
	if _, ok := comps[6].(discordgo.TextDisplay); !ok {
		t.Errorf("Component 6 should be TextDisplay, got %T", comps[6])
	}
	if _, ok := comps[7].(discordgo.ActionsRow); !ok {
		t.Errorf("Component 7 should be ActionsRow, got %T", comps[7])
	}
}

func TestShuffleQuizOptions(t *testing.T) {
	q := &QuizQuestion{
		Question:    "테스트 질문",
		Options:     []string{"A", "B", "C", "D"},
		AnswerIndex: 1, // "B"
		Explanation: "B가 정답",
	}

	correctText := q.Options[q.AnswerIndex]
	shuffleQuizOptions(q)

	if len(q.Options) != 4 {
		t.Fatalf("Expected 4 options, got %d", len(q.Options))
	}
	if q.AnswerIndex < 0 || q.AnswerIndex >= 4 {
		t.Fatalf("AnswerIndex %d out of bounds", q.AnswerIndex)
	}
	if q.Options[q.AnswerIndex] != correctText {
		t.Errorf("Option at AnswerIndex is %q, expected %q", q.Options[q.AnswerIndex], correctText)
	}

	// Statistical distribution test: over 1000 shuffles, verify all 4 indices appear frequently
	counts := make(map[int]int)
	for i := 0; i < 1000; i++ {
		testQ := &QuizQuestion{
			Question:    "Q",
			Options:     []string{"A", "B", "C", "D"},
			AnswerIndex: 0,
		}
		shuffleQuizOptions(testQ)
		counts[testQ.AnswerIndex]++
		if testQ.Options[testQ.AnswerIndex] != "A" {
			t.Fatalf("Mismatch after shuffle: %q != A", testQ.Options[testQ.AnswerIndex])
		}
	}
	for idx := 0; idx < 4; idx++ {
		if counts[idx] < 150 { // Expected ~250 each out of 1000
			t.Errorf("Index %d had unexpectedly low count: %d", idx, counts[idx])
		}
	}
}

func TestParseQuizQuestionsJSON(t *testing.T) {
	// Case 1: Wrapped format with multiple questions
	rawWrapped := `{
		"questions": [
			{
				"question": "1번 문제?",
				"options": ["A", "B", "C", "D"],
				"answer_index": 0,
				"explanation": "해설 1"
			},
			{
				"question": "2번 문제?",
				"options": ["가", "나", "다", "라"],
				"answer_index": 2,
				"explanation": "해설 2"
			}
		]
	}`
	qs, err := parseQuizQuestionsJSON(rawWrapped)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(qs) != 2 {
		t.Fatalf("Expected 2 questions, got %d", len(qs))
	}
	if qs[0].Question != "1번 문제?" || qs[1].AnswerIndex != 2 {
		t.Errorf("Parsed data mismatch: %+v, %+v", qs[0], qs[1])
	}

	// Case 2: Top-level array format
	rawArray := `[
		{
			"question": "문제 A",
			"options": ["1", "2", "3", "4"],
			"answer_index": 1,
			"explanation": "해설 A"
		}
	]`
	qs2, err := parseQuizQuestionsJSON(rawArray)
	if err != nil {
		t.Fatalf("Unexpected error for array: %v", err)
	}
	if len(qs2) != 1 || qs2[0].Question != "문제 A" {
		t.Errorf("Parsed array mismatch: %+v", qs2)
	}
}

func TestMultiQuestionComponents(t *testing.T) {
	sess := &ActiveQuizSession{
		ID:         "test_session",
		Topic:      "과학",
		Difficulty: "보통",
		Model:      "GPT-5.6 Terra",
		Questions: []*QuizQuestion{
			{
				Question:    "Q1",
				Options:     []string{"1", "2", "3", "4"},
				AnswerIndex: 0,
				Explanation: "E1",
			},
			{
				Question:    "Q2",
				Options:     []string{"A", "B", "C", "D"},
				AnswerIndex: 1,
				Explanation: "E2",
			},
		},
		CurrentIndex: 0,
		Score:        0,
		Answered:     false,
	}

	// 1. Question 1 components
	qComps := buildQuestionComponents(sess)
	if len(qComps) == 0 {
		t.Fatalf("Expected question components to be built")
	}

	// 2. Answer question 1 (Correct)
	sess.Answered = true
	sess.Score++
	sess.History = append(sess.History, true)
	ansComps := buildAnsweredComponents(sess, 0, "테스터")
	// Since 1 of 2 questions answered, there should be a Next Question button!
	foundNextBtn := false
	for _, c := range ansComps {
		if row, ok := c.(discordgo.ActionsRow); ok {
			for _, comp := range row.Components {
				if btn, ok := comp.(discordgo.Button); ok {
					if btn.CustomID == "quiz_next:test_session" {
						foundNextBtn = true
					}
				}
			}
		}
	}
	if !foundNextBtn {
		t.Errorf("Expected Next Question button when more questions remain")
	}

	// 3. Move to question 2 (Last question)
	sess.CurrentIndex = 1
	sess.Answered = true
	sess.History = append(sess.History, false)
	lastAnsComps := buildAnsweredComponents(sess, 2, "테스터")

	// Last question should NOT have Next Question button, but SHOULD have summary & Download button
	foundNextOnLast := false
	foundSummary := false
	foundDownloadOnLast := false
	for _, c := range lastAnsComps {
		if row, ok := c.(discordgo.ActionsRow); ok {
			for _, comp := range row.Components {
				if btn, ok := comp.(discordgo.Button); ok {
					if btn.CustomID == "quiz_next:test_session" {
						foundNextOnLast = true
					}
					if btn.CustomID == "quiz_download:test_session" {
						foundDownloadOnLast = true
					}
				}
			}
		}
		if td, ok := c.(discordgo.TextDisplay); ok {
			if strings.Contains(td.Content, "퀴즈 세션 완료") {
				foundSummary = true
			}
		}
	}
	if foundNextOnLast {
		t.Errorf("Did not expect Next Question button on last question")
	}
	if !foundDownloadOnLast {
		t.Errorf("Expected Download button on last question")
	}
	if !foundSummary {
		t.Errorf("Expected session completion summary on last question")
	}
}

func TestGenerateQuizReviewText(t *testing.T) {
	sess := &ActiveQuizSession{
		ID:         "test_review_session",
		UserID:     "user123",
		UserName:   "테스터",
		Topic:      "테스트 주제",
		Difficulty: "보통",
		Model:      "GPT-5.6 Terra",
		Questions: []*QuizQuestion{
			{
				Question:    "문제 1번 내용",
				Options:     []string{"A", "B", "C", "D"},
				AnswerIndex: 1, // B
				Explanation: "B가 정답인 해설입니다.",
			},
			{
				Question:    "문제 2번 내용",
				Options:     []string{"가", "나", "다", "라"},
				AnswerIndex: 3, // 라
				Explanation: "라가 정답인 해설입니다.",
			},
		},
		CurrentIndex: 1,
		Score:        1,
		Answered:     true,
		History:      []bool{true, false},
		UserChoices:  []int{1, 0}, // Q1: B (correct), Q2: 가 (wrong)
	}

	review := generateQuizReviewText(sess)

	if !strings.Contains(review, "[AI QUIZ REVIEW] 테스트 주제") {
		t.Errorf("Expected header with topic in review text")
	}
	if !strings.Contains(review, "풀이자: 테스터 | 최종 점수: 1 / 2 (50%)") {
		t.Errorf("Expected user and score in review text")
	}
	if !strings.Contains(review, "[문제 1] 문제 1번 내용") || !strings.Contains(review, "[문제 2] 문제 2번 내용") {
		t.Errorf("Expected questions in review text")
	}
	if !strings.Contains(review, "제출한 답: 2번 (B) [⭕ 정답]") {
		t.Errorf("Expected correct answer record for Q1 in review text")
	}
	if !strings.Contains(review, "제출한 답: 1번 (가) [❌ 오답]") {
		t.Errorf("Expected incorrect answer record for Q2 in review text")
	}
	if !strings.Contains(review, "실제 정답: 4번 (라)") {
		t.Errorf("Expected correct answer for Q2 in review text")
	}
	if !strings.Contains(review, "B가 정답인 해설입니다.") || !strings.Contains(review, "라가 정답인 해설입니다.") {
		t.Errorf("Expected explanations in review text")
	}
}

func TestSanitizeQuizFileName(t *testing.T) {
	cases := map[string]string{
		"파이썬 기초 문법":      "파이썬_기초_문법",
		"C++ / OOP & STL": "C_OOP_STL",
		"   spaces   ":    "spaces",
		"///":             "quiz",
	}

	for input, expected := range cases {
		actual := sanitizeQuizFileName(input)
		if actual != expected {
			t.Errorf("sanitizeQuizFileName(%q) = %q; want %q", input, actual, expected)
		}
	}
}

func TestReviewTextWithDifferentCreatorAndSolver(t *testing.T) {
	sess := &ActiveQuizSession{
		ID:         "test_solver_diff_session",
		UserID:     "creator_id",
		UserName:   "문제출제자",
		SolverID:   "solver_id",
		SolverName: "실제풀이자",
		Topic:      "한국사",
		Difficulty: "쉬움",
		Model:      "gemini-2.5-flash",
		Questions: []*QuizQuestion{
			{
				Question:    "조선의 제1대 왕은 누구인가요?",
				Options:     []string{"태조", "정종", "태종", "세종"},
				AnswerIndex: 0,
				Explanation: "조선의 제1대 왕은 태조 이성계입니다.",
			},
		},
		CurrentIndex: 0,
		Score:        1,
		Answered:     true,
		History:      []bool{true},
		UserChoices:  []int{0},
	}

	review := generateQuizReviewText(sess)

	if !strings.Contains(review, "풀이자: 실제풀이자") {
		t.Errorf("Expected review text to specify actual solver '실제풀이자'")
	}
	if !strings.Contains(review, "출제자: 문제출제자") {
		t.Errorf("Expected review text to specify creator '문제출제자'")
	}
}
