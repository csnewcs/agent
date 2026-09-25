package main

import (
	"bytes"
	"image"
	"image/color"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/fogleman/gg"
)

func TestRenderQuoteCard(t *testing.T) {
	msg := &discordgo.Message{
		Content:   "컬러 아바타가 있는 인용 이미지 테스트입니다.\n긴 문장도 카드 폭 안에서 자연스럽게 줄바꿈되어야 합니다.",
		Timestamp: time.Date(2026, 9, 21, 12, 34, 0, 0, time.UTC),
		Author:    &discordgo.User{Username: "quote_tester", GlobalName: "인용 테스터"},
	}
	data, err := renderQuoteCard(msg)
	if err != nil {
		t.Fatalf("renderQuoteCard() error = %v", err)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("quote card is not a valid image: %v", err)
	}
	if got := img.Bounds().Dx(); got != quoteCardWidth {
		t.Errorf("card width = %d, want %d", got, quoteCardWidth)
	}
	if got := img.Bounds().Dy(); got != quoteCardHeight {
		t.Errorf("card height = %d, want %d", got, quoteCardHeight)
	}
	lightPixels := 0
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := quoteCardWidth / 2; x < img.Bounds().Dx(); x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r > 0xc000 && g > 0xc000 && b > 0xc000 {
				lightPixels++
			}
		}
	}
	if lightPixels < 100 {
		t.Errorf("quote card has too few visible text pixels: %d", lightPixels)
	}
	if previewPath := os.Getenv("QUOTE_PREVIEW_PATH"); previewPath != "" {
		if err := os.WriteFile(previewPath, data, 0600); err != nil {
			t.Fatalf("write preview: %v", err)
		}
	}
}

func TestQuoteWrapTextAddsEllipsisWhenTruncated(t *testing.T) {
	face := newChainedFontFace(38).NewInstance()
	lines := quoteWrapText(face, strings.Repeat("아주 긴 인용문입니다 ", 30), 510, 3)
	if len(lines) != 3 {
		t.Fatalf("line count = %d, want 3", len(lines))
	}
	if !strings.HasSuffix(lines[len(lines)-1], "...") {
		t.Errorf("last line %q does not end with ellipsis", lines[len(lines)-1])
	}
}

func TestQuoteWrapTextBreaksAtSpaces(t *testing.T) {
	face := newChainedFontFace(44).NewInstance()
	lines := quoteWrapText(face, "“권한 다 뺏겨버려요오옷~♡”", 510, 4)
	if len(lines) != 2 {
		t.Fatalf("lines = %#v, want two lines", lines)
	}
	if lines[0] != "“권한 다" || lines[1] != "뺏겨버려요오옷~♡”" {
		t.Errorf("word wrapping split a word or orphaned the closing quote: %#v", lines)
	}
	for _, line := range lines {
		if width, _ := face.MeasureString(line); width > 510 {
			t.Errorf("line %q is %.1fpx wide, want at most 510px", line, width)
		}
	}
}

func TestQuoteWrapTextSplitsOnlyOverlongWords(t *testing.T) {
	face := newChainedFontFace(38).NewInstance()
	lines := quoteWrapText(face, "짧은 "+strings.Repeat("긴", 30), 160, 10)
	if lines[0] != "짧은" {
		t.Errorf("first line = %q, want a whole word", lines[0])
	}
	for _, line := range lines {
		if width, _ := face.MeasureString(line); width > 160 {
			t.Errorf("line %q is %.1fpx wide, want at most 160px", line, width)
		}
	}
}

func TestFormatQuoteText(t *testing.T) {
	if got := formatQuoteText("안녕하세요"); got != "“안녕하세요”" {
		t.Errorf("formatQuoteText() = %q", got)
	}
	if got := formatQuoteText("\"already quoted\""); got != "\"already quoted\"" {
		t.Errorf("formatQuoteText() changed an already quoted string: %q", got)
	}
}

func TestDrawQuoteAvatarWithFadeKeepsColor(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			src.SetRGBA(x, y, color.RGBA{R: 240, G: 40, B: 20, A: 255})
		}
	}
	dc := gg.NewContext(quoteCardWidth, quoteCardHeight)
	dc.SetColor(color.Black)
	dc.Clear()
	drawQuoteAvatarWithFade(dc, src, quoteCardWidth, quoteCardHeight)

	left := color.RGBAModel.Convert(dc.Image().At(100, quoteCardHeight/2)).(color.RGBA)
	if left.R <= left.G || left.R <= left.B {
		t.Fatalf("left avatar pixel lost its red color: %#v", left)
	}
	right := color.RGBAModel.Convert(dc.Image().At(620, quoteCardHeight/2)).(color.RGBA)
	if right.R != 0 || right.G != 0 || right.B != 0 {
		t.Fatalf("avatar did not fade to black at the right edge: %#v", right)
	}
}
