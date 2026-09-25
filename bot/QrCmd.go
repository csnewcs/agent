package main

import (
	"bytes"
	"fmt"
	"log/slog"
	"time"

	"github.com/bwmarrin/discordgo"
	qrcode "github.com/skip2/go-qrcode"
)

func buildQrCommand() (BotCommand, error) {
	return NewBotCommandBuilder("qr").
		WithDescription("m202436831<날짜><시각> 포맷의 모바일 출입 QR코드를 생성합니다.").
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionBoolean,
			Name:        "ephemeral",
			Description: "QR코드 비공개 여부 (기본값: 공개)",
			Required:    false,
		}).
		WithFunction(handleQrCommand).
		Build()
}

func buildQrCodeCommand() (BotCommand, error) {
	return NewBotCommandBuilder("qrcode").
		WithDescription("m202436831<날짜><시각> 포맷의 모바일 출입 QR코드를 생성합니다.").
		AddArg(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionBoolean,
			Name:        "ephemeral",
			Description: "QR코드 비공개 여부 (기본값: 공개)",
			Required:    false,
		}).
		WithFunction(handleQrCommand).
		Build()
}

func generateQRCodeString(t time.Time) string {
	kstLocation := time.FixedZone("KST", 9*3600)
	kstTime := t.In(kstLocation)
	return fmt.Sprintf("m202436831%s", kstTime.Format("20060102150405"))
}

func handleQrCommand(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	ephemeral := false
	options := ic.ApplicationCommandData().Options
	for _, opt := range options {
		if opt.Name == "ephemeral" {
			ephemeral = opt.BoolValue()
		}
	}

	kstLocation := time.FixedZone("KST", 9*3600)
	now := time.Now().In(kstLocation)
	code := generateQRCodeString(now)

	pngBytes, err := qrcode.Encode(code, qrcode.Medium, 512)
	if err != nil {
		slog.Error("Failed to encode QR code", "error", err, "code", code)
		comps := SimpleErrorCard("QR 코드 생성에 실패했습니다.")
		_ = RespondComponentsV2(s, ic, comps, true)
		return
	}

	builder := NewComponentsBuilder().
		WithImage("attachment://qr.png").
		WithFooter(fmt.Sprintf("발급 시각: %s (KST)", now.Format("2006-01-02 15:04:05")))

	comps := builder.Build()

	files := []*discordgo.File{
		{
			Name:        "qr.png",
			ContentType: "image/png",
			Reader:      bytes.NewReader(pngBytes),
		},
	}

	err = RespondComponentsV2WithFiles(s, ic, comps, files, ephemeral)
	if err != nil {
		slog.Error("Failed to respond to QR interaction", "error", err)
	}
}
