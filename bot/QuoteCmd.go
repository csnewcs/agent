package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/fogleman/gg"
	xdraw "golang.org/x/image/draw"
)

const (
	quoteCardWidth  = 1200
	quoteCardHeight = 630
)

func buildQuoteMessageCommand() (BotCommand, error) {
	return NewBotCommandBuilder("인용 이미지 만들기").
		WithType(discordgo.MessageApplicationCommand).
		WithFunction(handleQuoteMessageCommand).
		Build()
}

func handleQuoteMessageCommand(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	data := ic.ApplicationCommandData()
	target, ok := data.Resolved.Messages[data.TargetID]
	if !ok || target == nil || target.Author == nil {
		_ = RespondComponentsV2(s, ic, SimpleErrorCard("대상 메시지를 찾을 수 없습니다."), true)
		return
	}

	// Avatar download can take a moment, so acknowledge within Discord's interaction deadline.
	if err := s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsIsComponentsV2},
	}); err != nil {
		slog.Error("Failed to defer quote interaction", "error", err)
		return
	}

	card, err := renderQuoteCard(target)
	if err != nil {
		slog.Error("Failed to render quote card", "error", err)
		_, _ = editInteractionComponentsV2(s, ic, SimpleErrorCard("인용 이미지를 만드는 중 오류가 발생했습니다."))
		return
	}

	name := quoteAuthorName(target)
	comps := NewComponentsBuilder().
		WithTitle("인용 이미지").
		WithSubTitle(name).
		WithImage("attachment://quote.png").
		WithFooter("원본 메시지에서 생성됨").
		Build()
	emptyAttachments := []*discordgo.MessageAttachment{}
	appID := ic.Interaction.AppID
	if appID == "" && s.State != nil && s.State.User != nil {
		appID = s.State.User.ID
	}
	uri := discordgo.EndpointWebhookMessage(appID, ic.Interaction.Token, "@original")
	payload := quoteWebhookEditV2{
		Components:  &comps,
		Flags:       discordgo.MessageFlagsIsComponentsV2,
		Attachments: &emptyAttachments,
	}
	contentType, body, encodeErr := discordgo.MultipartBodyWithJSON(payload, []*discordgo.File{{
		Name: "quote.png", ContentType: "image/png", Reader: bytes.NewReader(card),
	}})
	if encodeErr != nil {
		slog.Error("Failed to encode quote card response", "error", encodeErr)
		return
	}
	_, err = s.RequestRaw("PATCH", uri, contentType, body, uri, 0)
	if err != nil {
		slog.Error("Failed to deliver quote card", "error", err)
	}
}

// Webhook edits need the Components V2 flag on every multipart PATCH. The
// discordgo WebhookEdit helper has no Flags field, so it cannot be used here.
type quoteWebhookEditV2 struct {
	Components  *[]discordgo.MessageComponent   `json:"components,omitempty"`
	Flags       discordgo.MessageFlags          `json:"flags"`
	Attachments *[]*discordgo.MessageAttachment `json:"attachments,omitempty"`
}

func quoteAuthorName(msg *discordgo.Message) string {
	if msg.Member != nil && strings.TrimSpace(msg.Member.Nick) != "" {
		return msg.Member.Nick
	}
	if msg.Author != nil {
		if strings.TrimSpace(msg.Author.GlobalName) != "" {
			return msg.Author.GlobalName
		}
		return msg.Author.Username
	}
	return "Unknown user"
}

func quoteBody(msg *discordgo.Message) string {
	body := strings.TrimSpace(msg.Content)
	if body != "" {
		return body
	}
	if len(msg.Attachments) > 0 {
		return "[첨부 파일이 있는 메시지]"
	}
	return "[텍스트 없는 메시지]"
}

func formatQuoteText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "“...”"
	}
	if !strings.HasPrefix(text, "“") && !strings.HasPrefix(text, "\"") {
		text = "“" + text
	}
	if !strings.HasSuffix(text, "”") && !strings.HasSuffix(text, "\"") {
		text = text + "”"
	}
	return text
}

func renderQuoteCard(msg *discordgo.Message) ([]byte, error) {
	dc := gg.NewContext(quoteCardWidth, quoteCardHeight)
	dc.SetColor(color.Black)
	dc.Clear()

	// 1. Draw avatar on the left in FULL COLOR, fading horizontally into black.
	avatarURL := ""
	if msg.Author != nil {
		avatarURL = msg.Author.AvatarURL("512")
	}
	avatar := quoteAvatar(avatarURL)
	drawQuoteAvatarWithFade(dc, avatar, quoteCardWidth, quoteCardHeight)

	// 2. Prepare quote text with standard curly quotes “ ... ”
	quoteText := formatQuoteText(quoteBody(msg))
	runes := []rune(quoteText)

	// Auto-scale font size depending on quote length
	var fontSize float64
	var lineHeight float64
	var maxLines int
	switch {
	case len(runes) <= 35:
		fontSize = 44
		lineHeight = 60
		maxLines = 4
	case len(runes) <= 90:
		fontSize = 38
		lineHeight = 52
		maxLines = 5
	case len(runes) <= 180:
		fontSize = 32
		lineHeight = 44
		maxLines = 6
	default:
		fontSize = 26
		lineHeight = 36
		maxLines = 8
	}

	textFace := newChainedFontFace(fontSize).NewInstance()
	const maxTextWidth = 510.0
	lines := quoteWrapText(textFace, quoteText, maxTextWidth, maxLines)

	nameFace := newChainedFontFace(25).NewInstance()
	handleFace := newChainedFontFace(18).NewInstance()

	authorName := "- " + quoteAuthorName(msg)
	authorName = nameFace.TruncateWithEllipsis(authorName, maxTextWidth)

	handle := ""
	if msg.Author != nil {
		handle = "@" + msg.Author.Username
	}
	if !msg.Timestamp.IsZero() {
		if handle != "" {
			handle += "  \u00b7  "
		}
		handle += msg.Timestamp.Local().Format("2006. 01. 02.")
	}
	if handle != "" {
		handle = handleFace.TruncateWithEllipsis(handle, maxTextWidth)
	}

	// 3. Compute vertical centering for the entire right-hand block
	// Make It A Quote center of text area: centreX = 900
	const centreX = 900.0
	textBlockHeight := float64(len(lines)) * lineHeight
	const gapToAuthor = 32.0
	const gapToHandle = 8.0
	nameHeight := 25.0
	handleHeight := 0.0
	if handle != "" {
		handleHeight = 18.0 + gapToHandle
	}
	totalHeight := textBlockHeight + gapToAuthor + nameHeight + handleHeight

	startY := (float64(quoteCardHeight) - totalHeight) / 2.0
	if startY < 30 {
		startY = 30
	}

	// 4. Draw Quote Lines (Pure White #FFFFFF, centered at centreX)
	dc.SetColor(color.White)
	for i, line := range lines {
		w, _ := textFace.MeasureString(line)
		lineX := centreX - w/2.0
		lineY := startY + float64(i)*lineHeight + fontSize*0.9
		textFace.DrawString(dc, line, lineX, lineY)
	}

	// 5. Draw Author Name (Pure White #FFFFFF, centered at centreX)
	authorY := startY + textBlockHeight + gapToAuthor + nameHeight*0.9
	nw, _ := nameFace.MeasureString(authorName)
	nameFace.DrawString(dc, authorName, centreX-nw/2.0, authorY)

	// 6. Draw Handle & Date (#9A9A9A muted gray, centered at centreX)
	if handle != "" {
		dc.SetColor(color.RGBA{154, 154, 154, 255})
		handleY := authorY + gapToHandle + 18.0
		hw, _ := handleFace.MeasureString(handle)
		handleFace.DrawString(dc, handle, centreX-hw/2.0, handleY)
	}

	var output bytes.Buffer
	if err := png.Encode(&output, dc.Image()); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func quoteWrapText(face *ChainedFaceInstance, text string, maxWidth float64, maxLines int) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var lines []string
	for _, paragraph := range strings.Split(text, "\n") {
		current := ""
		for _, word := range strings.Fields(paragraph) {
			if current != "" {
				candidate := current + " " + word
				if width, _ := face.MeasureString(candidate); width <= maxWidth {
					current = candidate
					continue
				}
				lines = append(lines, current)
				current = ""
			}

			// Only split inside a word when the word itself cannot fit.
			if width, _ := face.MeasureString(word); width <= maxWidth {
				current = word
				continue
			}
			for _, r := range word {
				candidate := current + string(r)
				if width, _ := face.MeasureString(candidate); width > maxWidth && current != "" {
					lines = append(lines, current)
					current = string(r)
				} else {
					current = candidate
				}
			}
			if len(lines) > maxLines {
				break
			}
		}
		if current != "" && len(lines) <= maxLines {
			lines = append(lines, current)
		}
		if len(lines) > maxLines {
			break
		}
	}
	if len(lines) == 0 {
		return []string{"“...”"}
	}
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[:maxLines]
		last := strings.TrimSuffix(lines[maxLines-1], "”")
		lines[maxLines-1] = face.TruncateWithEllipsis(last+"...", maxWidth)
	}
	return lines
}

func quoteAvatar(url string) image.Image {
	if url != "" {
		client := &http.Client{Timeout: 4 * time.Second}
		resp, err := client.Get(url)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				limited := io.LimitReader(resp.Body, 5<<20)
				if img, _, err := image.Decode(limited); err == nil {
					return img
				}
			}
		}
	}
	// Network failure should not prevent a quote from being created.
	dc := gg.NewContext(256, 256)
	grad := gg.NewLinearGradient(0, 0, 256, 256)
	grad.AddColorStop(0, color.RGBA{176, 132, 246, 255})
	grad.AddColorStop(1, color.RGBA{70, 54, 104, 255})
	dc.SetFillStyle(grad)
	dc.DrawRectangle(0, 0, 256, 256)
	dc.Fill()
	return dc.Image()
}

func drawQuoteAvatarWithFade(dc *gg.Context, src image.Image, cardW, cardH int) {
	if src == nil {
		return
	}
	srcB := src.Bounds()
	srcW := srcB.Dx()
	srcH := srcB.Dy()
	if srcW <= 0 || srcH <= 0 {
		return
	}

	avatarTargetSize := cardH // 630px

	// Aspect Fill: scale so that the smaller dimension fills avatarTargetSize
	scaleX := float64(avatarTargetSize) / float64(srcW)
	scaleY := float64(avatarTargetSize) / float64(srcH)
	scale := scaleX
	if scaleY > scale {
		scale = scaleY
	}
	scaledW := int(float64(srcW)*scale + 0.5)
	scaledH := int(float64(srcH)*scale + 0.5)
	if scaledW < avatarTargetSize {
		scaledW = avatarTargetSize
	}
	if scaledH < avatarTargetSize {
		scaledH = avatarTargetSize
	}

	scaled := image.NewRGBA(image.Rect(0, 0, scaledW, scaledH))
	xdraw.CatmullRom.Scale(scaled, scaled.Bounds(), src, src.Bounds(), xdraw.Over, nil)

	// Center crop to (avatarTargetSize, avatarTargetSize)
	cropped := image.NewRGBA(image.Rect(0, 0, avatarTargetSize, avatarTargetSize))
	cropX := (scaledW - avatarTargetSize) / 2
	cropY := (scaledH - avatarTargetSize) / 2
	xdraw.Copy(cropped, image.Pt(0, 0), scaled, image.Rect(cropX, cropY, cropX+avatarTargetSize, cropY+avatarTargetSize), xdraw.Src, nil)

	// Authentic Make It A Quote horizontal fade curve:
	// startRatio = 0.22 (264px on 1200px), endRatio = 0.50 (600px on 1200px)
	// stops: [0, 0], [0.55, 0.8], [1, 1]
	fadeStart := float64(cardW) * 0.22
	fadeEnd := float64(cardW) * 0.50
	if fadeEnd > float64(avatarTargetSize) {
		fadeEnd = float64(avatarTargetSize)
	}

	bounds := cropped.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			fx := float64(x)
			var alphaFactor float64
			if fx <= fadeStart {
				alphaFactor = 1.0
			} else if fx >= fadeEnd {
				alphaFactor = 0.0
			} else {
				pos := (fx - fadeStart) / (fadeEnd - fadeStart)
				if pos <= 0.55 {
					t := pos / 0.55
					st := t * t * (3.0 - 2.0*t)
					alphaFactor = 1.0 - st*0.8
				} else {
					t := (pos - 0.55) / 0.45
					st := t * t * (3.0 - 2.0*t)
					alphaFactor = 0.2 - st*0.2
				}
			}

			if alphaFactor <= 0.001 {
				cropped.SetRGBA(x, y, color.RGBA{0, 0, 0, 0})
				continue
			}

			// Keep avatar in FULL COLOR
			orig := cropped.RGBAAt(x, y)
			r := uint8(float64(orig.R) * alphaFactor)
			g := uint8(float64(orig.G) * alphaFactor)
			b := uint8(float64(orig.B) * alphaFactor)
			a := uint8(float64(orig.A) * alphaFactor)
			cropped.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: a})
		}
	}

	dc.DrawImage(cropped, 0, 0)
}
