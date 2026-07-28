package main

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"
)

type SafeBuffer struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

func (b *SafeBuffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *SafeBuffer) GetLastLines(n int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	str := b.buf.String()
	if str == "" {
		return "(출력 없음)"
	}
	lines := strings.Split(strings.TrimRight(str, "\r\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	res := strings.Join(lines, "\n")
	runes := []rune(res)
	if len(runes) > 990 {
		res = "..." + string(runes[len(runes)-987:])
	}
	return res
}

type CmdSession struct {
	ID       string
	OwnerID  string
	Command  string
	Cmd      *exec.Cmd
	Stdin    io.WriteCloser
	Buffer   *SafeBuffer
	Finished atomic.Bool
	IsKilled atomic.Bool
}

var activeCmdMap sync.Map // map[string]*CmdSession

func buildCmdEmbedAndComponents(cmdStr string, lastLines string, isFinished bool, isFailed bool, sessionID string) ([]*discordgo.MessageEmbed, []discordgo.MessageComponent) {
	title := "리눅스 커맨드 실행 중..."
	color := 0x3498db
	if isFinished {
		if isFailed {
			title = "리눅스 커맨드 실행 실패 / 중단됨"
			color = 0xe74c3c
		} else {
			title = "리눅스 커맨드 실행 완료"
			color = 0x2ecc71
		}
	}

	cmdRunes := []rune(cmdStr)
	if len(cmdRunes) > 990 {
		cmdStr = string(cmdRunes[:987]) + "..."
	}

	lastLinesRunes := []rune(lastLines)
	if len(lastLinesRunes) > 990 {
		lastLines = "..." + string(lastLinesRunes[len(lastLinesRunes)-987:])
	}

	embed := &discordgo.MessageEmbed{
		Title: title,
		Color: color,
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "명령어",
				Value:  fmt.Sprintf("```bash\n$ %s\n```", cmdStr),
				Inline: false,
			},
			{
				Name:   "최근 10줄 출력",
				Value:  fmt.Sprintf("```\n%s\n```", lastLines),
				Inline: false,
			},
		},
	}

	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "입력",
					Style:    discordgo.PrimaryButton,
					CustomID: "c_input:" + sessionID,
					Disabled: isFinished,
				},
				discordgo.Button{
					Label:    "중단",
					Style:    discordgo.DangerButton,
					CustomID: "c_stop:" + sessionID,
					Disabled: isFinished,
				},
			},
		},
	}

	return []*discordgo.MessageEmbed{embed}, components
}

func getUserID(ic *discordgo.InteractionCreate) string {
	if ic.User != nil {
		return ic.User.ID
	}
	if ic.Member != nil && ic.Member.User != nil {
		return ic.Member.User.ID
	}
	return ""
}

func handleCCommand(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	cmdStr := getInteractionOptionString(ic, "command")

	_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	ownerID := getUserID(ic)

	sessionID := fmt.Sprintf("%d", time.Now().UnixNano())

	execCmd := exec.Command("nsenter", "-t", "1", "-m", "-u", "-n", "-i", "bash", "-c", cmdStr)
	stdinPipe, err := execCmd.StdinPipe()
	if err != nil {
		slog.Error("Failed to get stdin pipe for /c", "error", err)
		errMsg := fmt.Sprintf("Error: %v", err)
		_, _ = s.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{
			Content: &errMsg,
		})
		return
	}

	buf := &SafeBuffer{}
	execCmd.Stdout = buf
	execCmd.Stderr = buf

	cmdSession := &CmdSession{
		ID:      sessionID,
		OwnerID: ownerID,
		Command: cmdStr,
		Cmd:     execCmd,
		Stdin:   stdinPipe,
		Buffer:  buf,
	}

	activeCmdMap.Store(sessionID, cmdSession)

	if err := execCmd.Start(); err != nil {
		activeCmdMap.Delete(sessionID)
		errMsg := fmt.Sprintf("Failed to start command: %v", err)
		_, editErr := s.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{
			Content: &errMsg,
		})
		if editErr != nil {
			slog.Error("Failed to edit interaction response on start error", "error", editErr)
		}
		return
	}

	// 명령어 시작 직후 즉시 첫 임베드를 전송하여 디스코드에 "생각 중..." 대기가 길어지지 않게 함
	initialLastLines := buf.GetLastLines(10)
	initialEmbeds, initialComponents := buildCmdEmbedAndComponents(cmdStr, initialLastLines, false, false, sessionID)
	_, editErr := s.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{
		Embeds:     &initialEmbeds,
		Components: &initialComponents,
	})
	if editErr != nil {
		slog.Error("Failed to send initial /c interaction response edit", "error", editErr)
	}

	go func() {
		defer activeCmdMap.Delete(sessionID)

		waitDone := make(chan error, 1)
		go func() {
			waitDone <- execCmd.Wait()
		}()

		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case err := <-waitDone:
				cmdSession.Finished.Store(true)
				isFailed := (err != nil) || cmdSession.IsKilled.Load()
				lastLines := buf.GetLastLines(10)
				embeds, components := buildCmdEmbedAndComponents(cmdStr, lastLines, true, isFailed, sessionID)
				_, editErr := s.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{
					Embeds:     &embeds,
					Components: &components,
				})
				if editErr != nil {
					slog.Error("Failed to send final /c interaction response edit", "error", editErr)
				}
				return

			case <-ticker.C:
				lastLines := buf.GetLastLines(10)
				embeds, components := buildCmdEmbedAndComponents(cmdStr, lastLines, false, false, sessionID)
				_, editErr := s.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{
					Embeds:     &embeds,
					Components: &components,
				})
				if editErr != nil {
					slog.Error("Failed to send ticker /c interaction response edit", "error", editErr)
				}
			}
		}
	}()
}

func HandleCmdComponent(session *discordgo.Session, ic *discordgo.InteractionCreate) {
	customID := ic.MessageComponentData().CustomID
	slog.Info("CmdComponent interaction received", "custom_id", customID)

	var sessionID string
	var action string
	if strings.HasPrefix(customID, "c_input:") {
		sessionID = strings.TrimPrefix(customID, "c_input:")
		action = "input"
	} else if strings.HasPrefix(customID, "c_stop:") {
		sessionID = strings.TrimPrefix(customID, "c_stop:")
		action = "stop"
	} else {
		return
	}

	val, ok := activeCmdMap.Load(sessionID)
	if !ok {
		slog.Warn("Cmd session not found or already finished", "session_id", sessionID)
		_ = session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "이미 종료되었거나 존재하지 않는 실행 세션입니다.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	cmdSession := val.(*CmdSession)

	userID := getUserID(ic)
	if userID != cmdSession.OwnerID && cmdSession.OwnerID != "" {
		slog.Warn("Cmd interaction user mismatch", "user_id", userID, "owner_id", cmdSession.OwnerID)
		_ = session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "명령어를 실행한 사용자만 조작할 수 있습니다.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	if action == "input" {
		err := session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseModal,
			Data: &discordgo.InteractionResponseData{
				CustomID: "c_modal:" + sessionID,
				Title:    "커맨드 표준 입력 (stdin)",
				Components: []discordgo.MessageComponent{
					discordgo.ActionsRow{
						Components: []discordgo.MessageComponent{
							discordgo.TextInput{
								CustomID:    "input_text",
								Label:       "전송할 텍스트",
								Style:       discordgo.TextInputShort,
								Placeholder: "예: y, n, password...",
								Required:    true,
							},
						},
					},
				},
			},
		})
		if err != nil {
			slog.Error("Failed to open stdin modal", "error", err)
		}
	} else if action == "stop" {
		slog.Info("Stopping command session", "session_id", sessionID)
		cmdSession.IsKilled.Store(true)
		if cmdSession.Cmd != nil && cmdSession.Cmd.Process != nil {
			err := cmdSession.Cmd.Process.Kill()
			if err != nil {
				slog.Error("Failed to kill process", "error", err)
			}
		}
		err := session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredMessageUpdate,
		})
		if err != nil {
			slog.Error("Failed to respond to stop button interaction", "error", err)
		}
	}
}

func HandleCmdModalSubmit(session *discordgo.Session, ic *discordgo.InteractionCreate) {
	customID := ic.ModalSubmitData().CustomID
	slog.Info("CmdModalSubmit interaction received", "custom_id", customID)

	if !strings.HasPrefix(customID, "c_modal:") {
		return
	}

	sessionID := strings.TrimPrefix(customID, "c_modal:")
	val, ok := activeCmdMap.Load(sessionID)
	if !ok {
		_ = session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "이미 종료되었거나 존재하지 않는 실행 세션입니다.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	cmdSession := val.(*CmdSession)

	userID := getUserID(ic)
	if userID != cmdSession.OwnerID && cmdSession.OwnerID != "" {
		_ = session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "명령어를 실행한 사용자만 조작할 수 있습니다.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	var inputText string
	for _, comp := range ic.ModalSubmitData().Components {
		if ar, ok := comp.(*discordgo.ActionsRow); ok {
			for _, c := range ar.Components {
				if ti, ok := c.(*discordgo.TextInput); ok && ti.CustomID == "input_text" {
					inputText = ti.Value
				}
			}
		}
	}

	if cmdSession.Stdin != nil {
		_, err := cmdSession.Stdin.Write([]byte(inputText + "\n"))
		if err != nil {
			slog.Error("Failed to write to stdin pipe", "error", err)
		} else {
			slog.Info("Wrote text to stdin pipe", "session_id", sessionID, "len", len(inputText))
		}
	}

	_ = session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	})
}
