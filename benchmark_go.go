package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"os"
	"sync"
	"time"

	"github.com/disintegration/gift"
	"github.com/fogleman/gg"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
)

type LyricItem struct {
	TimeMs   int
	Text     string
	Trans    string
	Phonetic string
}

func main() {
	width := 1600
	height := 640
	durationMs := 160000

	fontPath := "/mnt/antigravity_workspaces/ai-agent/bot/assets/fonts/NotoSansKR.ttf"
	fontBytes, err := os.ReadFile(fontPath)
	if err != nil {
		fmt.Printf("Read font error: %v\n", err)
		return
	}

	otFont, err := opentype.Parse(fontBytes)
	if err != nil {
		fmt.Printf("Font parse error: %v\n", err)
		return
	}

	var sampleLines []LyricItem
	for i := 0; i < 40; i++ {
		sampleLines = append(sampleLines, LyricItem{
			TimeMs:   i * 4000,
			Text:     fmt.Sprintf("Line %d: 眠れない夜の静寂を切り裂いていく", i+1),
			Trans:    fmt.Sprintf("%d번째 줄: 잠들지 못하는 밤의 적막을 가르고 나아가", i+1),
			Phonetic: fmt.Sprintf("nemurenai yoru no shijima o kirisaite iku %d", i+1),
		})
	}

	fmt.Println("=================================================================")
	fmt.Println("🚀 Pure Go (Golang) 가사 카드 렌더링 벤치마크 테스트")
	fmt.Printf("• 해상도: %dx%d | 폰트: NotoSansKR.ttf\n", width, height)
	fmt.Printf("• 테스트 프레임 수: %d장\n", len(sampleLines))
	fmt.Println("=================================================================")

	// 1. Base Background generation (Blur + Tint)
	t0 := time.Now()
	rawImg := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(rawImg, rawImg.Bounds(), &image.Uniform{color.RGBA{45, 30, 60, 255}}, image.Point{}, draw.Src)

	g := gift.New(
		gift.GaussianBlur(22),
		gift.Colorize(240, 10, -30),
	)
	baseBg := image.NewRGBA(g.Bounds(rawImg.Bounds()))
	g.Draw(baseBg, rawImg)
	bgTime := time.Since(t0)

	// 2. On-demand single frame render
	renderSingle := func(idx int, outPath string) error {
		dc := gg.NewContext(width, height)
		dc.DrawImage(baseBg, 0, 0)

		cur := sampleLines[idx]

		// Progress bar
		progRatio := float64(cur.TimeMs) / float64(durationMs)
		if progRatio > 1.0 {
			progRatio = 1.0
		}
		progW := progRatio * float64(width)
		dc.SetRGB255(235, 135, 158)
		dc.DrawRectangle(0, 0, progW, 7)
		dc.Fill()

		dc.SetRGBA255(55, 55, 60, 160)
		dc.DrawRectangle(progW, 0, float64(width)-progW, 7)
		dc.Fill()

		leftX := 75.0

		// Local thread-safe font faces
		faceDim, _ := opentype.NewFace(otFont, &opentype.FaceOptions{Size: 26, DPI: 72, Hinting: font.HintingNone})
		faceDimSmall, _ := opentype.NewFace(otFont, &opentype.FaceOptions{Size: 23, DPI: 72, Hinting: font.HintingNone})
		faceMain, _ := opentype.NewFace(otFont, &opentype.FaceOptions{Size: 42, DPI: 72, Hinting: font.HintingNone})
		faceSub, _ := opentype.NewFace(otFont, &opentype.FaceOptions{Size: 26, DPI: 72, Hinting: font.HintingNone})
		facePhonetic, _ := opentype.NewFace(otFont, &opentype.FaceOptions{Size: 24, DPI: 72, Hinting: font.HintingNone})

		// Previous Lines
		if idx >= 2 {
			dc.SetFontFace(faceDimSmall)
			dc.SetRGBA255(125, 125, 130, 140)
			dc.DrawString(sampleLines[idx-2].Text, leftX, 85)
		}
		if idx >= 1 {
			dc.SetFontFace(faceDim)
			dc.SetRGBA255(145, 145, 150, 180)
			dc.DrawString(sampleLines[idx-1].Text, leftX, 130)
		}

		// Active Line
		curY := 210.0
		dc.SetFontFace(faceMain)
		dc.SetRGB255(255, 255, 255)
		dc.DrawString(cur.Text, leftX, curY)
		curY += 58.0

		if cur.Trans != "" {
			dc.SetFontFace(faceSub)
			dc.SetRGBA255(185, 185, 192, 230)
			dc.DrawString(cur.Trans, leftX, curY)
			curY += 42.0
		}

		if cur.Phonetic != "" {
			dc.SetFontFace(facePhonetic)
			dc.SetRGB255(238, 162, 175)
			dc.DrawString(cur.Phonetic, leftX, curY)
			curY += 40.0
		}

		// Next Lines
		nextY := curY + 20.0
		if nextY < 420.0 {
			nextY = 420.0
		}
		if idx+1 < len(sampleLines) {
			dc.SetFontFace(faceDim)
			dc.SetRGBA255(145, 145, 150, 180)
			dc.DrawString(sampleLines[idx+1].Text, leftX, nextY)
			nextY += 44.0
		}
		if idx+2 < len(sampleLines) {
			dc.SetFontFace(faceDimSmall)
			dc.SetRGBA255(125, 125, 130, 140)
			dc.DrawString(sampleLines[idx+2].Text, leftX, nextY)
		}

		if outPath != "" {
			f, err := os.Create(outPath)
			if err != nil {
				return err
			}
			defer f.Close()
			return jpeg.Encode(f, dc.Image(), &jpeg.Options{Quality: 85})
		}
		return nil
	}

	// Benchmark 1 frame in Go
	t0 = time.Now()
	_ = renderSingle(1, "/tmp/go_lyrics_sample.jpg")
	singleGoTime := time.Since(t0)

	// Benchmark 40 frames batch in Go (using Goroutines)
	t0 = time.Now()
	var wg sync.WaitGroup
	for i := 0; i < len(sampleLines); i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_ = renderSingle(idx, fmt.Sprintf("/tmp/go_frame_%03d.jpg", idx))
		}(i)
	}
	wg.Wait()
	batchGoTime := time.Since(t0)

	fi, _ := os.Stat("/tmp/go_lyrics_sample.jpg")
	var fileSizeKB float64
	if fi != nil {
		fileSizeKB = float64(fi.Size()) / 1024.0
	}

	fmt.Printf("• 배경 블러 1회 초기화 시간 : %.2f ms (곡 시작 시 1회)\n", float64(bgTime.Microseconds())/1000.0)
	fmt.Println("-----------------------------------------------------------------")
	fmt.Printf("[1] Pure Go 1코어 온디맨드 1장 렌더링 : %.2f ms (파일 크기: %.1f KB)\n", float64(singleGoTime.Microseconds())/1000.0, fileSizeKB)
	fmt.Printf("[2] Pure Go 고루틴 병렬 40장 일괄 렌더링: %.2f ms (%.2f 초)\n", float64(batchGoTime.Microseconds())/1000.0, batchGoTime.Seconds())
	fmt.Printf("    └─ 고루틴 병렬 장당 평균 시간       : %.2f ms\n", float64(batchGoTime.Microseconds())/1000.0/float64(len(sampleLines)))
	fmt.Println("=================================================================")
}
