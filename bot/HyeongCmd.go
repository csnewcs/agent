package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

func buildHyeongCommand() (BotCommand, error) {
	return NewBotCommandBuilder("혀엉").
		WithDescription(".").
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionSubCommand,
			Name:        "인코드",
			Description: ".",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "text",
					Description: ".",
					Required:    true,
				},
			},
		}).
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionSubCommand,
			Name:        "디코드",
			Description: ".",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "code",
					Description: ".",
					Required:    true,
				},
			},
		}).
		WithFunction(handleHyeongCommand).
		Build()
}

func buildHyeongMessageEncodeCommand() (BotCommand, error) {
	return NewBotCommandBuilder("혀엉 인코드").
		WithType(discordgo.MessageApplicationCommand).
		WithFunction(handleHyeongMessageEncode).
		Build()
}

func buildHyeongMessageDecodeCommand() (BotCommand, error) {
	return NewBotCommandBuilder("혀엉 디코드").
		WithType(discordgo.MessageApplicationCommand).
		WithFunction(handleHyeongMessageDecode).
		Build()
}

func handleHyeongMessageEncode(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	data := ic.ApplicationCommandData()
	targetMsg, ok := data.Resolved.Messages[data.TargetID]
	if !ok || targetMsg == nil {
		comps := SimpleErrorCard("대상 메시지를 찾을 수 없습니다.")
		_ = RespondComponentsV2(s, ic, comps, true)
		return
	}

	text := strings.TrimSpace(targetMsg.Content)
	if text == "" {
		comps := SimpleErrorCard("인코딩할 텍스트 내용이 없는 메시지입니다.")
		_ = RespondComponentsV2(s, ic, comps, true)
		return
	}

	encodedCode := encodeHyeongOnlineOrLocal(text)

	if len(encodedCode) > 1950 {
		fileBuf := bytes.NewBufferString(encodedCode)
		comps := NewComponentsBuilder().
			WithTitle("혀엉... 인코딩 결과").
			WithBody("코드가 길어 파일로 첨부되었습니다.").
			WithFooter("Hyeong Esoteric Language").
			Build()

		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Components: comps,
				Flags:      discordgo.MessageFlagsIsComponentsV2,
				Files: []*discordgo.File{
					{
						Name:        "hyeong_code.txt",
						ContentType: "text/plain; charset=utf-8",
						Reader:      fileBuf,
					},
				},
			},
		})
	} else {
		comps := NewComponentsBuilder().
			WithTitle("혀엉... 인코딩 결과").
			WithBody(fmt.Sprintf("```\n%s\n```", encodedCode)).
			WithFooter("Hyeong Esoteric Language").
			Build()

		_ = RespondComponentsV2(s, ic, comps, false)
	}
}

func handleHyeongMessageDecode(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	data := ic.ApplicationCommandData()
	targetMsg, ok := data.Resolved.Messages[data.TargetID]
	if !ok || targetMsg == nil {
		comps := SimpleErrorCard("대상 메시지를 찾을 수 없습니다.")
		_ = RespondComponentsV2(s, ic, comps, false)
		return
	}

	code := targetMsg.Content
	decodedText, err := decodeHyeongOnlineOrLocal(code)
	if err != nil {
		comps := SimpleErrorCard(fmt.Sprintf("디코딩 실행 중 오류가 발생했습니다: %v", err))
		_ = RespondComponentsV2(s, ic, comps, false)
		return
	}

	if decodedText == "" {
		decodedText = "(출력 없음)"
	}

	comps := NewComponentsBuilder().
		WithTitle("혀엉... 디코딩 결과").
		WithBody(fmt.Sprintf("```\n%s\n```", decodedText)).
		WithFooter("Hyeong Esoteric Language").
		Build()

	_ = RespondComponentsV2(s, ic, comps, false)
}

func handleHyeongCommand(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	options := ic.ApplicationCommandData().Options
	if len(options) == 0 {
		comps := SimpleErrorCard("서브커맨드(`인코드` 또는 `디코드`)를 선택해주세요.")
		_ = RespondComponentsV2(s, ic, comps, true)
		return
	}

	subCmd := options[0]

	switch subCmd.Name {
	case "인코드":
		text := ""
		for _, opt := range subCmd.Options {
			if opt.Name == "text" {
				text = opt.StringValue()
			}
		}

		if text == "" {
			comps := SimpleErrorCard("인코딩할 텍스트를 입력해주세요.")
			_ = RespondComponentsV2(s, ic, comps, true)
			return
		}

		encodedCode := encodeHyeongOnlineOrLocal(text)

		if len(encodedCode) > 1950 {
			fileBuf := bytes.NewBufferString(encodedCode)
			comps := NewComponentsBuilder().
				WithTitle("혀엉... 인코딩 결과").
				WithBody("코드가 길어 파일로 첨부되었습니다.").
				WithFooter("Hyeong Esoteric Language").
				Build()

			_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Components: comps,
					Flags:      discordgo.MessageFlagsIsComponentsV2,
					Files: []*discordgo.File{
						{
							Name:        "hyeong_code.txt",
							ContentType: "text/plain; charset=utf-8",
							Reader:      fileBuf,
						},
					},
				},
			})
		} else {
			comps := NewComponentsBuilder().
				WithTitle("혀엉... 인코딩 결과").
				WithBody(fmt.Sprintf("```\n%s\n```", encodedCode)).
				WithFooter("Hyeong Esoteric Language").
				Build()

			_ = RespondComponentsV2(s, ic, comps, false)
		}

	case "디코드":
		code := ""
		for _, opt := range subCmd.Options {
			if opt.Name == "code" {
				code = opt.StringValue()
			}
		}

		if code == "" {
			comps := SimpleErrorCard("디코딩할 '혀엉...' 코드를 입력해주세요.")
			_ = RespondComponentsV2(s, ic, comps, true)
			return
		}

		decodedText, err := decodeHyeongOnlineOrLocal(code)
		if err != nil {
			comps := SimpleErrorCard(fmt.Sprintf("디코딩 실행 중 오류가 발생했습니다: %v", err))
			_ = RespondComponentsV2(s, ic, comps, false)
			return
		}

		if decodedText == "" {
			decodedText = "(출력 없음)"
		}

		comps := NewComponentsBuilder().
			WithTitle("혀엉... 디코딩 결과").
			WithBody(fmt.Sprintf("```\n%s\n```", decodedText)).
			WithFooter("Hyeong Esoteric Language").
			Build()

		_ = RespondComponentsV2(s, ic, comps, false)
	}
}

// -------------------------------------------------------------
// Online API with Local Fallback (Matching azestkingscrown.com)
// -------------------------------------------------------------

func encodeHyeongOnlineOrLocal(text string) string {
	// Try azestkingscrown.com /api/translate
	apiURL := "https://xn--boy-1t5a.azestkingscrown.com/api/translate"
	reqPayload, _ := json.Marshal(map[string]string{"text": text})
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(reqPayload))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; DiscordBot/1.0)")
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == 200 {
			defer resp.Body.Close()
			var res struct {
				Code string `json:"code"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&res); err == nil && res.Code != "" {
				return res.Code
			}
		}
	}

	// Local fallback encoder
	return EncodeHyeongTextLocal(text)
}

func decodeHyeongOnlineOrLocal(code string) (string, error) {
	// Try azestkingscrown.com /api/run
	apiURL := "https://xn--boy-1t5a.azestkingscrown.com/api/run"
	reqPayload, _ := json.Marshal(map[string]string{"code": code})
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(reqPayload))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; DiscordBot/1.0)")
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == 200 {
			defer resp.Body.Close()
			var res struct {
				Output      string `json:"output"`
				ErrorOutput string `json:"error_output"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&res); err == nil {
				if res.ErrorOutput != "" && res.Output == "" {
					return "", fmt.Errorf("%s", res.ErrorOutput)
				}
				return res.Output, nil
			}
		}
	}

	// Local fallback interpreter matching exact spec
	return DecodeHyeongCodeLocal(code)
}

// -------------------------------------------------------------
// Pure Go Native Hyeo-ung Encoder & VM Interpreter
// -------------------------------------------------------------

func numToHyeo(val int) string {
	if val == 0 {
		return "형."
	}
	for d := 15; d >= 1; d-- {
		if val%d == 0 {
			c := val / d
			if c >= 1 && c <= 70 {
				var syl string
				if c == 1 {
					syl = "형"
				} else {
					syl = "혀" + strings.Repeat("어", c-2) + "엉"
				}
				return syl + strings.Repeat(".", d)
			}
		}
	}
	var syl string
	if val == 1 {
		syl = "형"
	} else {
		syl = "혀" + strings.Repeat("어", val-2) + "엉"
	}
	return syl + "."
}

func encodeHyeongChar(r rune) string {
	n := int(r)
	if n <= 150 {
		return fmt.Sprintf("%s 흑... 항.", numToHyeo(n))
	}
	D := 42
	q := n / D
	E := n % D
	B := 42
	A := q / B
	C := q % B

	return fmt.Sprintf("%s %s 하앗... %s 하앙... %s 하앗... %s 하앙... 흑... 항.",
		numToHyeo(A), numToHyeo(B), numToHyeo(C), numToHyeo(D), numToHyeo(E))
}

func EncodeHyeongTextLocal(text string) string {
	var parts []string
	for _, r := range text {
		parts = append(parts, encodeHyeongChar(r))
	}
	parts = append(parts, "흑. 흑")
	return strings.Join(parts, " ")
}

type hyeongToken struct {
	Syllables string
	CharCount int
	Dots      int
}

func DecodeHyeongCodeLocal(code string) (string, error) {
	code = strings.ReplaceAll(code, "…", "...")
	code = strings.ReplaceAll(code, "⋯", "...")
	code = strings.ReplaceAll(code, "⋮", "...")

	runes := []rune(code)
	n := len(runes)
	var tokens []hyeongToken

	i := 0
	for i < n {
		c := runes[i]
		if strings.ContainsRune("형항핫흣흡흑혀하흐", c) {
			start := i
			if c == '형' || strings.ContainsRune("항핫흣흡흑", c) {
				i++
			} else if c == '혀' {
				for i < n && runes[i] != '엉' {
					i++
				}
				if i < n {
					i++
				}
			} else if c == '하' {
				for i < n && !strings.ContainsRune("앙앗", runes[i]) {
					i++
				}
				if i < n {
					i++
				}
			} else if c == '흐' {
				for i < n && !strings.ContainsRune("읏읍윽", runes[i]) {
					i++
				}
				if i < n {
					i++
				}
			}

			syllables := string(runes[start:i])
			charCount := len([]rune(syllables))

			dotStart := i
			for i < n && runes[i] == '.' {
				i++
			}
			dots := i - dotStart

			tokens = append(tokens, hyeongToken{
				Syllables: syllables,
				CharCount: charCount,
				Dots:      dots,
			})
		} else {
			i++
		}
	}

	stacks := make(map[int][]int)
	curStack := 3
	var outRunes []rune

	stepLimit := 100000
	steps := 0

	for _, tok := range tokens {
		steps++
		if steps > stepLimit {
			break
		}

		sylRunes := []rune(tok.Syllables)
		if len(sylRunes) == 0 {
			continue
		}
		lead := sylRunes[0]
		tail := sylRunes[len(sylRunes)-1]
		count := tok.CharCount
		dots := tok.Dots

		if lead == '형' || (lead == '혀' && tail == '엉') {
			stacks[curStack] = append(stacks[curStack], count*dots)
		} else if lead == '항' || (lead == '하' && tail == '앙') {
			sumVal := 0
			for j := 0; j < count; j++ {
				st := stacks[curStack]
				if len(st) > 0 {
					sumVal += st[len(st)-1]
					stacks[curStack] = st[:len(st)-1]
				}
			}
			if dots == 1 {
				if sumVal >= 0 && sumVal <= 0x10FFFF {
					outRunes = append(outRunes, rune(sumVal))
				}
			} else {
				stacks[dots] = append(stacks[dots], sumVal)
			}
		} else if lead == '핫' || (lead == '하' && tail == '앗') {
			mulVal := 1
			for j := 0; j < count; j++ {
				st := stacks[curStack]
				if len(st) > 0 {
					mulVal *= st[len(st)-1]
					stacks[curStack] = st[:len(st)-1]
				}
			}
			if dots == 1 {
				if mulVal >= 0 && mulVal <= 0x10FFFF {
					outRunes = append(outRunes, rune(mulVal))
				}
			} else {
				stacks[dots] = append(stacks[dots], mulVal)
			}
		} else if lead == '흣' || (lead == '흐' && tail == '읏') {
			var popped []int
			for j := 0; j < count; j++ {
				st := stacks[curStack]
				if len(st) > 0 {
					popped = append(popped, -st[len(st)-1])
					stacks[curStack] = st[:len(st)-1]
				} else {
					popped = append(popped, 0)
				}
			}
			for j := len(popped) - 1; j >= 0; j-- {
				stacks[curStack] = append(stacks[curStack], popped[j])
			}
			sumVal := 0
			for _, p := range popped {
				sumVal += p
			}
			if dots == 1 {
				if sumVal >= 0 && sumVal <= 0x10FFFF {
					outRunes = append(outRunes, rune(sumVal))
				}
			} else {
				stacks[dots] = append(stacks[dots], sumVal)
			}
		} else if lead == '흑' || (lead == '흐' && tail == '윽') {
			top := 0
			st := stacks[curStack]
			if len(st) > 0 {
				top = st[len(st)-1]
				stacks[curStack] = st[:len(st)-1]
			}
			for j := 0; j < count; j++ {
				stacks[dots] = append(stacks[dots], top)
			}
			stacks[curStack] = append(stacks[curStack], top)
			curStack = dots
		}
	}

	return string(outRunes), nil
}
