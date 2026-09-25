package main

import (
	"encoding/json"
	"fmt"
	"html"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	// Code block regex: matches ```lang\ncode``` (handles code blocks to preserve them intact)
	reCodeBlock = regexp.MustCompile("(?s)```[^\n]*\n.*?```|```.*?```")

	// HTML conversions
	reHTMLPreCode = regexp.MustCompile(`(?is)<pre>\s*<code(?:\s+class=["'](?:language-)?([a-zA-Z0-9_-]+)["'])?>([\s\S]*?)</code>\s*</pre>`)
	reHTMLPre     = regexp.MustCompile(`(?is)<pre>([\s\S]*?)</pre>`)
	reHTMLCode    = regexp.MustCompile(`(?is)<code>([\s\S]*?)</code>`)
	reHTMLBold    = regexp.MustCompile(`(?is)<(?:b|strong)\b[^>]*>(.*?)</(?:b|strong)>`)
	reHTMLItalic  = regexp.MustCompile(`(?is)<(?:i|em)\b[^>]*>(.*?)</(?:i|em)>`)
	reHTMLStrike  = regexp.MustCompile(`(?is)<(?:del|s|strike)\b[^>]*>(.*?)</(?:del|s|strike)>`)
	reHTMLUnder   = regexp.MustCompile(`(?is)<(?:u|ins)\b[^>]*>(.*?)</(?:u|ins)>`)
	reHTMLBreak   = regexp.MustCompile(`(?i)<br\s*/?>`)
	reHTMLHR      = regexp.MustCompile(`(?i)<hr\s*/?>`)
	reHTMLP       = regexp.MustCompile(`(?is)<p\b[^>]*>(.*?)</p>`)
	reHTMLH1H2    = regexp.MustCompile(`(?is)<h[12]\b[^>]*>(.*?)</h[12]>`)
	reHTMLH3H6    = regexp.MustCompile(`(?is)<h[3-6]\b[^>]*>(.*?)</h[3-6]>`)
	reHTMLLink    = regexp.MustCompile(`(?is)<a\s+(?:[^>]*?\s+)?href=["']([^"']+)["'][^>]*>(.*?)</a>`)
	reHTMLTags    = regexp.MustCompile(`(?i)<(?:\/?[a-zA-Z][a-zA-Z0-9-]*\b[^>]*|\/)>`)

	// Escaped markdown fixing
	reEscapedMarkdown = regexp.MustCompile(`\\([*#_~` + "`" + `|\[\]()!<>\\-])`)

	// Headings > 3 not supported by Discord
	reDeepHeadings = regexp.MustCompile(`(?m)^(\s*)#{4,6}\s+(.*)$`)

	// Discord spacing issues (e.g. ** text ** does not bold in Discord)
	reBoldSpace   = regexp.MustCompile(`\*\*(\s*)([^\*\n]+?)(\s*)\*\*`)
	reStrikeSpace = regexp.MustCompile(`~~(\s*)([^~\n]+?)(\s*)~~`)
	reUnderSpace  = regexp.MustCompile(`__(\s*)([^_\n]+?)(\s*)__`)

	// Markdown images ![alt](url) -> Discord can't render inline images
	reMarkdownImage = regexp.MustCompile(`!\[(.*?)\]\((https?://[^\s\)]+)\)`)

	// Local file links [label](file:///...) -> Discord cannot open file:// links, strip into plain label
	reFileLink    = regexp.MustCompile(`\[([^\]]+)\]\(file:///[^\)]+\)`)
	reBareFileURI = regexp.MustCompile(`file:///([^\s\)\>]+)`)

	// Tasks / Checkboxes
	reTaskUnchecked = regexp.MustCompile(`(?m)^(\s*)[-*]\s+\[\s*\]\s+`)
	reTaskChecked   = regexp.MustCompile(`(?m)^(\s*)[-*]\s+\[[xX]\]\s+`)
)

// FormatDiscordMarkdown inspects AI-generated markdown and adapts it for Discord:
// 1. Preserves existing valid code blocks verbatim.
// 2. Converts HTML formatting tags (<b>, <i>, <code>, <pre>, <br>, etc.) to Discord Markdown.
// 3. Converts markdown tables into monospace code blocks so columns align in Discord.
// 4. Normalizes deep headings (#### -> ###) which Discord does not support.
// 5. Fixes spaced markdown markers (e.g. "** bold **" -> " **bold** ").
// 6. Unescapes accidentally escaped markdown tags (\*\* -> **).
// 7. Converts markdown image syntax to links.
// 8. Converts task lists to checkbox icons (☐, ☑).
// 9. Filters/strips unsupported raw HTML tags and balances unclosed formatting delimiters.
func FormatDiscordMarkdown(input string) string {
	input = CleanResultOutput(input)
	if strings.TrimSpace(input) == "" {
		return input
	}

	// 1. Split input into code blocks and normal text segments to protect code blocks
	segments := splitIntoCodeSegments(input)

	var sb strings.Builder
	for _, seg := range segments {
		if seg.isCodeBlock {
			// Ensure code block itself is well-formed
			sb.WriteString(seg.content)
		} else {
			// Process normal text segment
			processed := formatNormalTextSegment(seg.content)
			sb.WriteString(processed)
		}
	}

	result := sb.String()

	// 2. Balance unclosed delimiters (e.g., dangling **, ```, `, etc.)
	result = balanceMarkdownDelimiters(result)

	return result
}

type textSegment struct {
	content     string
	isCodeBlock bool
}

func splitIntoCodeSegments(input string) []textSegment {
	var segments []textSegment
	matches := reCodeBlock.FindAllStringIndex(input, -1)

	lastIdx := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		if start > lastIdx {
			segments = append(segments, textSegment{
				content:     input[lastIdx:start],
				isCodeBlock: false,
			})
		}
		segments = append(segments, textSegment{
			content:     input[start:end],
			isCodeBlock: true,
		})
		lastIdx = end
	}

	if lastIdx < len(input) {
		segments = append(segments, textSegment{
			content:     input[lastIdx:],
			isCodeBlock: false,
		})
	}

	return segments
}

func formatNormalTextSegment(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}

	// 1. Convert HTML tags to Discord markdown
	text = convertHTMLToMarkdown(text)

	// 2. Unescape escaped markdown formatting (\*\*bold\*\* -> **bold**)
	text = reEscapedMarkdown.ReplaceAllString(text, "$1")

	// 3. Normalize unsupported headings (####, #####, ###### -> ###)
	text = reDeepHeadings.ReplaceAllString(text, "${1}### ${2}")

	// 4. Fix spaced markdown markers (e.g., "** text **" -> " **text** ")
	text = fixDiscordMarkdownSpaces(text)

	// 5. Convert Markdown image syntax with local paths or URLs
	reLocalImg := regexp.MustCompile(`!\[(.*?)\]\((?:file:\/\/)?(\/(?:home\/fedora|\.gemini|mnt\/antigravity_workspaces|tmp)[^\)\s]+)\)`)
	text = reLocalImg.ReplaceAllStringFunc(text, func(m string) string {
		sub := reLocalImg.FindStringSubmatch(m)
		if len(sub) >= 3 {
			alt := strings.TrimSpace(sub[1])
			fp := strings.TrimSpace(sub[2])
			fn := filepath.Base(fp)
			if alt != "" {
				return fmt.Sprintf("**%s** (%s)", alt, fn)
			}
			return fmt.Sprintf("**%s**", fn)
		}
		return m
	})

	text = reMarkdownImage.ReplaceAllStringFunc(text, func(m string) string {
		sub := reMarkdownImage.FindStringSubmatch(m)
		if len(sub) >= 3 {
			alt := strings.TrimSpace(sub[1])
			url := strings.TrimSpace(sub[2])
			if alt != "" {
				return fmt.Sprintf("[%s](%s)", alt, url)
			}
			return url
		}
		return m
	})

	// 6. Convert local file:// URIs (Discord does not support file:// links)
	text = reFileLink.ReplaceAllString(text, "**$1**")
	text = reBareFileURI.ReplaceAllString(text, "/$1")

	// 7. Convert task list checkboxes
	text = reTaskChecked.ReplaceAllString(text, "${1}- ☑ ")
	text = reTaskUnchecked.ReplaceAllString(text, "${1}- ☐ ")

	// 8. Convert Markdown tables to aligned code blocks
	text = convertMarkdownTables(text)

	// 9. Strip remaining unsupported HTML tags
	text = reHTMLTags.ReplaceAllString(text, "")

	// 10. Decode HTML entities (&amp;, &lt;, &gt;, &quot;, &#39;, &nbsp;)
	text = html.UnescapeString(text)

	return text
}

func convertHTMLToMarkdown(text string) string {
	// Pre / Code blocks
	text = reHTMLPreCode.ReplaceAllStringFunc(text, func(m string) string {
		sub := reHTMLPreCode.FindStringSubmatch(m)
		if len(sub) >= 3 {
			lang := sub[1]
			code := strings.Trim(sub[2], "\r\n")
			return fmt.Sprintf("```%s\n%s\n```", lang, code)
		}
		return m
	})

	text = reHTMLPre.ReplaceAllStringFunc(text, func(m string) string {
		sub := reHTMLPre.FindStringSubmatch(m)
		if len(sub) >= 2 {
			code := strings.Trim(sub[1], "\r\n")
			return fmt.Sprintf("```\n%s\n```", code)
		}
		return m
	})

	// Inline code
	text = reHTMLCode.ReplaceAllString(text, "`$1`")

	// Headings
	text = reHTMLH1H2.ReplaceAllString(text, "\n## $1\n")
	text = reHTMLH3H6.ReplaceAllString(text, "\n### $1\n")

	// Bold, Italic, Strikethrough, Underline
	text = reHTMLBold.ReplaceAllString(text, "**$1**")
	text = reHTMLItalic.ReplaceAllString(text, "*$1*")
	text = reHTMLStrike.ReplaceAllString(text, "~~$1~~")
	text = reHTMLUnder.ReplaceAllString(text, "__$1__")

	// Links
	text = reHTMLLink.ReplaceAllString(text, "[$2]($1)")

	// Line breaks and Paragraphs
	text = reHTMLBreak.ReplaceAllString(text, "\n")
	text = reHTMLHR.ReplaceAllString(text, "\n──────────\n")
	text = reHTMLP.ReplaceAllString(text, "\n$1\n")

	return text
}

func fixDiscordMarkdownSpaces(text string) string {
	// "** hello **" -> " **hello** "
	text = reBoldSpace.ReplaceAllStringFunc(text, func(m string) string {
		sub := reBoldSpace.FindStringSubmatch(m)
		if len(sub) >= 4 {
			lead := sub[1]
			body := strings.TrimSpace(sub[2])
			trail := sub[3]
			if body == "" {
				return m
			}
			return lead + "**" + body + "**" + trail
		}
		return m
	})

	// "~~ hello ~~" -> " ~~hello~~ "
	text = reStrikeSpace.ReplaceAllStringFunc(text, func(m string) string {
		sub := reStrikeSpace.FindStringSubmatch(m)
		if len(sub) >= 4 {
			lead := sub[1]
			body := strings.TrimSpace(sub[2])
			trail := sub[3]
			if body == "" {
				return m
			}
			return lead + "~~" + body + "~~" + trail
		}
		return m
	})

	// "__ hello __" -> " __hello__ "
	text = reUnderSpace.ReplaceAllStringFunc(text, func(m string) string {
		sub := reUnderSpace.FindStringSubmatch(m)
		if len(sub) >= 4 {
			lead := sub[1]
			body := strings.TrimSpace(sub[2])
			trail := sub[3]
			if body == "" {
				return m
			}
			return lead + "__" + body + "__" + trail
		}
		return m
	})

	return text
}

// convertMarkdownTables detects markdown tables in plain text and wraps them in code blocks
func convertMarkdownTables(text string) string {
	lines := strings.Split(text, "\n")
	var result []string

	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// Check if line looks like a table row: has '|'
		if isTableRow(trimmed) && i+1 < len(lines) && isTableSeparatorRow(strings.TrimSpace(lines[i+1])) {
			// Found table start! Collect all consecutive table rows
			var tableLines []string
			for i < len(lines) && isTableRow(strings.TrimSpace(lines[i])) {
				tableLines = append(tableLines, lines[i])
				i++
			}

			// Format table lines nicely and wrap in code block
			formattedTable := alignMarkdownTable(tableLines)
			result = append(result, "```")
			result = append(result, formattedTable...)
			result = append(result, "```")
			continue
		}

		result = append(result, line)
		i++
	}

	return strings.Join(result, "\n")
}

func isTableRow(line string) bool {
	if !strings.Contains(line, "|") {
		return false
	}
	// Row must have at least one column separator or be enclosed in pipes
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "|") || strings.Count(trimmed, "|") >= 2
}

func isTableSeparatorRow(line string) bool {
	if !strings.Contains(line, "|") || !strings.Contains(line, "-") {
		return false
	}
	// Check that row contains only |, -, :, and spaces
	for _, r := range line {
		if r != '|' && r != '-' && r != ':' && r != ' ' && r != '\t' {
			return false
		}
	}
	return true
}

func alignMarkdownTable(tableLines []string) []string {
	if len(tableLines) < 2 {
		return tableLines
	}

	var rows [][]string
	for _, line := range tableLines {
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimPrefix(trimmed, "|")
		trimmed = strings.TrimSuffix(trimmed, "|")
		cells := strings.Split(trimmed, "|")
		for j := range cells {
			cells[j] = strings.TrimSpace(cells[j])
		}
		rows = append(rows, cells)
	}

	// Calculate max cols and column widths
	maxCols := 0
	for _, r := range rows {
		if len(r) > maxCols {
			maxCols = len(r)
		}
	}

	if maxCols == 0 {
		return tableLines
	}

	colWidths := make([]int, maxCols)
	for rIdx, r := range rows {
		if rIdx == 1 && isTableSeparatorRow(tableLines[1]) {
			continue // skip separator row in width calculation
		}
		for cIdx, cell := range r {
			cellLen := len([]rune(cell))
			if cellLen > colWidths[cIdx] {
				colWidths[cIdx] = cellLen
			}
		}
	}

	// Ensure minimum width of 3 for each column
	for cIdx := range colWidths {
		if colWidths[cIdx] < 3 {
			colWidths[cIdx] = 3
		}
	}

	var output []string
	for rIdx, r := range rows {
		if rIdx == 1 && isTableSeparatorRow(tableLines[1]) {
			// Separator row
			var sepCells []string
			for cIdx := 0; cIdx < maxCols; cIdx++ {
				sepCells = append(sepCells, strings.Repeat("-", colWidths[cIdx]))
			}
			output = append(output, "| "+strings.Join(sepCells, " | ")+" |")
			continue
		}

		var rowCells []string
		for cIdx := 0; cIdx < maxCols; cIdx++ {
			val := ""
			if cIdx < len(r) {
				val = r[cIdx]
			}
			valRunes := len([]rune(val))
			padding := colWidths[cIdx] - valRunes
			if padding < 0 {
				padding = 0
			}
			rowCells = append(rowCells, val+strings.Repeat(" ", padding))
		}
		output = append(output, "| "+strings.Join(rowCells, " | ")+" |")
	}

	return output
}

func balanceMarkdownDelimiters(text string) string {
	// 1. Triple backticks ```
	backtick3Count := strings.Count(text, "```")
	if backtick3Count%2 != 0 {
		text += "\n```"
	}

	// 2. Bold asterisks **
	// Count occurrences of ** outside code blocks
	var outsideSb strings.Builder
	parts := strings.Split(text, "```")
	for i, p := range parts {
		if i%2 == 0 { // outside code block
			outsideSb.WriteString(p)
		}
	}
	outsideText := outsideSb.String()

	boldCount := strings.Count(outsideText, "**")
	if boldCount%2 != 0 {
		text += "**"
	}

	strikeCount := strings.Count(outsideText, "~~")
	if strikeCount%2 != 0 {
		text += "~~"
	}

	spoilerCount := strings.Count(outsideText, "||")
	if spoilerCount%2 != 0 {
		text += "||"
	}

	return text
}

// FormatThinkingMarkdown formats model reasoning/thought logs cleanly for Discord blockquote display
func FormatThinkingMarkdown(thinking string, maxRunes int) string {
	if strings.TrimSpace(thinking) == "" {
		return ""
	}

	// 1. Convert code blocks inside thinking to avoid breaking Discord blockquotes
	thinking = strings.ReplaceAll(thinking, "```", "`")

	// 2. Truncate to maxRunes cleanly if exceeded
	runes := []rune(thinking)
	if len(runes) > maxRunes {
		startIndex := len(runes) - maxRunes
		sub := string(runes[startIndex:])
		if idx := strings.Index(sub, "\n"); idx != -1 && idx < 80 {
			thinking = "..." + sub[idx+1:]
		} else {
			thinking = "..." + sub
		}
	}

	// 3. Format into Discord blockquotes
	lines := strings.Split(strings.TrimSpace(thinking), "\n")
	var quotedLines []string
	for _, l := range lines {
		trimmed := strings.TrimRight(l, " \r\t")
		if trimmed == "" {
			quotedLines = append(quotedLines, ">")
		} else {
			quotedLines = append(quotedLines, "> "+trimmed)
		}
	}

	return fmt.Sprintf("> **Thinking**\n%s", strings.Join(quotedLines, "\n"))
}

// CleanResultOutput extracts the inner response text if the input is a serialized JSON
// object or Python dictionary representation (e.g. {'conversation_id': '...', 'status': 'SUCCESS', 'response': '...'}).
func CleanResultOutput(input string) string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return input
	}

	// 1. JSON check
	if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(trimmed), &m); err == nil {
			for _, key := range []string{"response", "content", "text", "output", "message"} {
				if val, ok := m[key]; ok && val != nil {
					if s, ok := val.(string); ok && s != "" {
						return CleanResultOutput(s)
					}
				}
			}
		}
	}

	// 2. Python dict representation check
	if (strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) ||
		strings.Contains(trimmed, "'conversation_id'") ||
		strings.Contains(trimmed, "\"conversation_id\"") {

		keys := []string{"'response':", "\"response\":", "'content':", "\"content\":", "'text':", "\"text\":"}
		var keyIdx int = -1
		var keyLen int = 0
		for _, k := range keys {
			if idx := strings.Index(trimmed, k); idx != -1 {
				keyIdx = idx
				keyLen = len(k)
				break
			}
		}

		if keyIdx != -1 {
			rest := strings.TrimLeft(trimmed[keyIdx+keyLen:], " \t\r\n")
			if len(rest) > 0 {
				quoteChar := rest[0]
				if quoteChar == '\'' || quoteChar == '"' {
					valContent := rest[1:]
					endIdx := -1
					numBackslashes := 0
					for i := 0; i < len(valContent); i++ {
						c := valContent[i]
						if c == '\\' {
							numBackslashes++
						} else {
							if c == quoteChar && numBackslashes%2 == 0 {
								endIdx = i
								break
							}
							numBackslashes = 0
						}
					}

					if endIdx != -1 {
						extracted := valContent[:endIdx]
						return unescapePythonString(extracted)
					}
				}
			}
		}
	}

	return input
}

func unescapePythonString(s string) string {
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			next := s[i+1]
			switch next {
			case 'n':
				sb.WriteByte('\n')
				i++
			case 'r':
				sb.WriteByte('\r')
			case 't':
				sb.WriteByte('\t')
				i++
			case '\\':
				sb.WriteByte('\\')
				i++
			case '\'':
				sb.WriteByte('\'')
				i++
			case '"':
				sb.WriteByte('"')
				i++
			case 'u':
				if i+5 < len(s) {
					if r, err := strconv.ParseInt(s[i+2:i+6], 16, 32); err == nil {
						sb.WriteRune(rune(r))
						i += 5
						continue
					}
				}
				sb.WriteByte('\\')
			default:
				sb.WriteByte('\\')
			}
		} else {
			sb.WriteByte(s[i])
		}
	}
	return sb.String()
}
