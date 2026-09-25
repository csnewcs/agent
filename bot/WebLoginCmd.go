package main

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/bwmarrin/discordgo"
)

func buildWebLoginCommand() (BotCommand, error) {
	return NewBotCommandBuilder("web_login").
		WithDescription("Antigravity 웹 콘솔 로그인을 위한 일회용 인증 코드를 발급합니다.").
		WithFunction(handleWebLoginCommand).
		Build()
}

func generateSecure6DigitCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return fmt.Sprintf("%06d", time.Now().UnixNano()%1000000)
	}
	return fmt.Sprintf("%06d", n.Int64())
}

func handleWebLoginCommand(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	var user *discordgo.User
	if ic.Member != nil && ic.Member.User != nil {
		user = ic.Member.User
	} else if ic.User != nil {
		user = ic.User
	}

	if user == nil {
		comps := SimpleErrorCard("사용자 정보를 확인할 수 없습니다.")
		_ = RespondComponentsV2(s, ic, comps, true)
		return
	}

	code := generateSecure6DigitCode()
	avatarUrl := user.AvatarURL("128")
	if avatarUrl == "" {
		avatarUrl = fmt.Sprintf("https://cdn.discordapp.com/embed/avatars/%d.png", (time.Now().Unix())%6)
	}

	globalName := user.GlobalName
	if globalName == "" {
		globalName = user.Username
	}

	token := &WebLoginToken{
		Code:       code,
		UserID:     user.ID,
		Username:   user.Username,
		GlobalName: globalName,
		AvatarURL:  avatarUrl,
		CreatedAt:  time.Now(),
	}

	if redisClient != nil {
		if err := redisClient.SaveWebLoginToken(token, 5*time.Minute); err != nil {
			slog.Error("Failed to save web login token to Redis", "error", err)
			comps := SimpleErrorCard("인증 토큰 생성 중 Redis 오류가 발생했습니다.")
			_ = RespondComponentsV2(s, ic, comps, true)
			return
		}
	} else {
		comps := SimpleErrorCard("Redis 서버 연결이 초기화되지 않았습니다.")
		_ = RespondComponentsV2(s, ic, comps, true)
		return
	}

	loginBtn := discordgo.Button{
		Label: "웹 콘솔 바로가기",
		Style: discordgo.LinkButton,
		URL:   "https://antigravity.csnewcs.dev",
	}

	dmSuccess := false
	dmChannel, err := s.UserChannelCreate(user.ID)
	if err == nil && dmChannel != nil {
		dmComps := NewComponentsBuilder().
			WithTitle("Antigravity 웹 콘솔 로그인 일회용 코드").
			WithBody(fmt.Sprintf("# `%s`\n\n• 유효 시간: **5분 (300초)**\n• 웹 로그인 페이지에서 위 6자리 인증 코드를 입력하세요.", code)).
			WithFooter("보안을 위해 타인에게 인증 코드를 공유하지 마세요.").
			WithButtons(loginBtn).
			Build()

		_, sendErr := s.ChannelMessageSendComplex(dmChannel.ID, &discordgo.MessageSend{
			Components: dmComps,
			Flags:      discordgo.MessageFlagsIsComponentsV2,
		})
		if sendErr == nil {
			dmSuccess = true
		} else {
			slog.Warn("Failed to send DM for web login code", "user_id", user.ID, "error", sendErr)
		}
	}

	if dmSuccess {
		comps := NewComponentsBuilder().
			WithTitle("DM 발송 완료").
			WithBody("DM(개인 메시지)으로 웹 콘솔 로그인 인증 코드가 발송되었습니다!\n브라우저에서 6자리 코드를 입력해 주세요.").
			WithFooter("유효 시간: 5분").
			WithButtons(loginBtn).
			Build()

		_ = RespondComponentsV2(s, ic, comps, true)
	} else {
		comps := NewComponentsBuilder().
			WithTitle("Antigravity 웹 콘솔 로그인 인증 코드").
			WithBody(fmt.Sprintf("# `%s`\n\n• 유효 시간: **5분 (300초)**\n• 웹 콘솔 로그인 페이지에 입력하세요.", code)).
			WithFooter("DM 발송 실패로 비공개 메시지로 출력됨").
			WithButtons(loginBtn).
			Build()

		_ = RespondComponentsV2(s, ic, comps, true)
	}
}
