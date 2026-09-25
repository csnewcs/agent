package main

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestFormatDiscordMarkdown_HTMLTags(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Bold HTML tags",
			input:    "이것은 <b>굵은 글씨</b>와 <strong>강조 글씨</strong>입니다.",
			expected: "이것은 **굵은 글씨**와 **강조 글씨**입니다.",
		},
		{
			name:     "Italic & Strike HTML tags",
			input:    "<i>기울임</i> 및 <s>취소선</s> 및 <code>코드</code>",
			expected: "*기울임* 및 ~~취소선~~ 및 `코드`",
		},
		{
			name:     "HTML Pre block",
			input:    "<pre><code>hello_world()</code></pre>",
			expected: "```\nhello_world()\n```",
		},
		{
			name:     "HTML Breaks and Entities",
			input:    "첫 줄<br>둘째 줄 &amp; &lt;셋째 줄&gt;",
			expected: "첫 줄\n둘째 줄 & <셋째 줄>",
		},
		{
			name:     "HTML Link",
			input:    `<a href="https://example.com">클릭 링크</a>`,
			expected: "[클릭 링크](https://example.com)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := FormatDiscordMarkdown(tt.input)
			if actual != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, actual)
			}
		})
	}
}

func TestFormatDiscordMarkdown_EscapedAndSpaced(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Escaped asterisks",
			input:    `이것은 \*\*굵게\*\* 표시되어야 합니다.`,
			expected: "이것은 **굵게** 표시되어야 합니다.",
		},
		{
			name:     "Spaced asterisks in bold",
			input:    "이것은 ** 굵은 텍스트 ** 입니다.",
			expected: "이것은  **굵은 텍스트**  입니다.",
		},
		{
			name:     "Deep heading 4",
			input:    "#### 서브헤더 제목",
			expected: "### 서브헤더 제목",
		},
		{
			name:     "Checkboxes",
			input:    "- [ ] 해야할 일\n- [x] 완료된 일",
			expected: "- ☐ 해야할 일\n- ☑ 완료된 일",
		},
		{
			name:     "Markdown Image",
			input:    "![미리보기 이미지](https://example.com/image.png)",
			expected: "[미리보기 이미지](https://example.com/image.png)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := FormatDiscordMarkdown(tt.input)
			if actual != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, actual)
			}
		})
	}
}

func TestFormatDiscordMarkdown_CodeBlockPreservation(t *testing.T) {
	input := "일반 텍스트 <b>굵게</b>\n```python\n# <b>태그 유지되어야 함</b>\nx = '**굵게 안됨**'\n```\n끝 <strong>강조</strong>"
	expected := "일반 텍스트 **굵게**\n```python\n# <b>태그 유지되어야 함</b>\nx = '**굵게 안됨**'\n```\n끝 **강조**"

	actual := FormatDiscordMarkdown(input)
	if actual != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, actual)
	}
}

func TestFormatDiscordMarkdown_TableConversion(t *testing.T) {
	input := "다음은 표입니다:\n| 이름 | 점수 |\n|---|---|\n| 철수 | 100 |\n| 영희 | 95 |\n이상입니다."
	actual := FormatDiscordMarkdown(input)

	if !strings.Contains(actual, "```") {
		t.Errorf("expected table to be wrapped in code block, got: %s", actual)
	}
	if !strings.Contains(actual, "철수") || !strings.Contains(actual, "100") {
		t.Errorf("expected table content preserved, got: %s", actual)
	}
}

func TestFormatDiscordMarkdown_DelimiterBalancing(t *testing.T) {
	// Unclosed code block
	input1 := "시작\n```go\nfmt.Println(1)"
	actual1 := FormatDiscordMarkdown(input1)
	if !strings.HasSuffix(actual1, "```") {
		t.Errorf("expected closed code block, got: %s", actual1)
	}

	// Unclosed bold
	input2 := "이것은 **굵은 글씨 시작"
	actual2 := FormatDiscordMarkdown(input2)
	if !strings.HasSuffix(actual2, "**") {
		t.Errorf("expected closed bold, got: %s", actual2)
	}
}

func TestFormatThinkingMarkdown(t *testing.T) {
	input := "Thinking about solution...\n```go\ncode_here()\n```\nDone."
	actual := FormatThinkingMarkdown(input, 500)

	if strings.Contains(actual, "```") {
		t.Errorf("thinking should not contain code block fences, got: %s", actual)
	}
	if !strings.HasPrefix(actual, "> **Thinking**\n") {
		t.Errorf("expected thinking prefix, got: %s", actual)
	}
}

func TestFormatDiscordMarkdown_FileLinks(t *testing.T) {
	input := "자세한 내용은 [봇 소스코드](file:///mnt/antigravity_workspaces/ai-agent/bot) 및 [`AntigravityMarkdown.go`](file:///mnt/antigravity_workspaces/ai-agent/bot/AntigravityMarkdown.go#L10-L20) 참고"
	expected := "자세한 내용은 **봇 소스코드** 및 **`AntigravityMarkdown.go`** 참고"
	actual := FormatDiscordMarkdown(input)
	if actual != expected {
		t.Errorf("expected %q, got %q", expected, actual)
	}
}

func TestBuildAntigravityMessageContent_ComponentsV2(t *testing.T) {
	sess := &AntigravitySessionState{
		SessionID:     "ag_proj_test",
		TurnID:        "turn_123",
		ProjectID:     "test-project",
		Model:         "gemini-3.7-flash-high",
		Prompt:        "테스트 질문입니다",
		Status:        "running",
		ThinkingLogs:  []string{"thinking 1", "thinking 2"},
		RawToolNames:  []string{"view_file", "view_file", "run_command"},
	}

	_, followups, comps := buildAntigravityMessageContent(sess)
	if len(comps) == 0 {
		t.Fatalf("expected components to be non-empty")
	}
	if len(followups) != 0 {
		t.Errorf("expected no followups for short content")
	}

	// Verify top-level components structure
	// Should have TextDisplay (title), Separator, TextDisplay (body), Separator, TextDisplay (footer), ActionsRow (button)
	hasTitle := false
	hasActionsRow := false
	for _, c := range comps {
		if td, ok := c.(discordgo.TextDisplay); ok {
			if strings.Contains(td.Content, "Antigravity Agent") {
				hasTitle = true
			}
		}
		if ar, ok := c.(discordgo.ActionsRow); ok {
			if len(ar.Components) > 0 {
				hasActionsRow = true
			}
		}
	}

	if !hasTitle {
		t.Errorf("expected title TextDisplay in components")
	}
	if !hasActionsRow {
		t.Errorf("expected ActionsRow with cancel button in components")
	}

	// Verify simple components builder
	simpleComps := buildSimpleComponentsV2("초기화", "내용", "푸터")
	if len(simpleComps) == 0 {
		t.Fatalf("expected simpleComps to be non-empty")
	}

	// Verify token usage footer has -# on every line
	sessWithUsage := &AntigravitySessionState{
		SessionID: "ag_proj_test2",
		Prompt:    "토큰 테스트",
		Status:    "completed",
		FinalResponse: "응답 완료",
		Usage: TokenUsage{
			TotalTokens:       5000,
			InputTokens:       3000,
			OutputTokens:      1500,
			ThinkingTokens:    500,
			CacheReadTokens:   1000,
		},
	}
	_, _, usageComps := buildAntigravityMessageContent(sessWithUsage)
	foundFooter := false
	for _, c := range usageComps {
		if td, ok := c.(discordgo.TextDisplay); ok {
			if strings.Contains(td.Content, "사용 토큰") {
				foundFooter = true
				lines := strings.Split(td.Content, "\n")
				for _, line := range lines {
					if !strings.HasPrefix(strings.TrimSpace(line), "-# ") {
						t.Errorf("expected line to start with -#, got: %s", line)
					}
				}
			}
		}
	}
	if !foundFooter {
		t.Errorf("expected footer with token usage to be found")
	}
}

func TestCleanResultOutput(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "User reported Python dict raw result",
			input:    `{'conversation_id': '21657542-e68e-4846-90d1-8e15530aa269', 'status': 'SUCCESS', 'response': '네, 정상적으로 살아있고 대기 중입니다! 필요한 작업이 있으시면 말씀해 주세요.\n', 'duration_seconds': 1125701.871387238, 'num_turns': 23, 'usage': {'input_tokens': 9820199, 'output_tokens': 355325, 'thinking_tokens': 182004, 'cache_read_tokens': 118603375, 'total_tokens': 10175524}}`,
			expected: "네, 정상적으로 살아있고 대기 중입니다! 필요한 작업이 있으시면 말씀해 주세요.\n",
		},
		{
			name:     "Standard JSON result object",
			input:    `{"conversation_id": "test-uuid", "status": "SUCCESS", "response": "안녕하세요!\n반갑습니다."}`,
			expected: "안녕하세요!\n반갑습니다.",
		},
		{
			name:     "Plain normal text",
			input:    "이것은 일반 마크다운 응답입니다.",
			expected: "이것은 일반 마크다운 응답입니다.",
		},
		{
			name:     "Python dict with escaped quotes and unicode",
			input:    `{'response': 'It\'s a "test" with \u0020space\nDone!'}`,
			expected: "It's a \"test\" with  space\nDone!",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := CleanResultOutput(tt.input)
			if actual != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, actual)
			}
		})
	}
}
