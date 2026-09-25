package main

import (
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/disintegration/gift"
	"github.com/fogleman/gg"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

type FontPair struct {
	sfntFont *sfnt.Font
	otFont   *opentype.Font
}

type ChainedFontFace struct {
	fonts []FontPair
	size  float64
}

var (
	loadedFontPairs []FontPair
	fontInitOnce    sync.Once
)

func getFontCandidates() []string {
	return []string{
		"assets/fonts/Paperlogy-8ExtraBold.ttf",
		"/app/assets/fonts/Paperlogy-8ExtraBold.ttf",
		"/mnt/antigravity_workspaces/ai-agent/bot/assets/fonts/Paperlogy-8ExtraBold.ttf",

		"assets/fonts/NotoSansKR-Bold.ttf",
		"/app/assets/fonts/NotoSansKR-Bold.ttf",
		"/mnt/antigravity_workspaces/ai-agent/bot/assets/fonts/NotoSansKR-Bold.ttf",

		"assets/fonts/NotoSansJP-Bold.ttf",
		"/app/assets/fonts/NotoSansJP-Bold.ttf",
		"/mnt/antigravity_workspaces/ai-agent/bot/assets/fonts/NotoSansJP-Bold.ttf",

		"assets/fonts/NotoSansKR.ttf",
		"/app/assets/fonts/NotoSansKR.ttf",
		"/mnt/antigravity_workspaces/ai-agent/bot/assets/fonts/NotoSansKR.ttf",
	}
}

func initLyricsFonts() {
	fontInitOnce.Do(func() {
		candidates := getFontCandidates()
		seen := make(map[string]bool)

		for _, path := range candidates {
			base := filepath.Base(path)
			if seen[base] {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			sFont, err := sfnt.Parse(data)
			if err != nil {
				continue
			}
			otFont, err := opentype.Parse(data)
			if err != nil {
				continue
			}
			loadedFontPairs = append(loadedFontPairs, FontPair{
				sfntFont: sFont,
				otFont:   otFont,
			})
			seen[base] = true
			slog.Info("Loaded font for Go Lyrics Renderer", "font", base, "size_kb", len(data)/1024)
		}
	})
}

func newChainedFontFace(size float64) *ChainedFontFace {
	initLyricsFonts()
	return &ChainedFontFace{
		fonts: loadedFontPairs,
		size:  size,
	}
}

type ChainedFaceInstance struct {
	faces []font.Face
	sFonts []*sfnt.Font
	buf   sfnt.Buffer
}

func (cf *ChainedFontFace) NewInstance() *ChainedFaceInstance {
	inst := &ChainedFaceInstance{
		sFonts: make([]*sfnt.Font, len(cf.fonts)),
		faces:  make([]font.Face, len(cf.fonts)),
	}
	for i, pair := range cf.fonts {
		inst.sFonts[i] = pair.sfntFont
		face, err := opentype.NewFace(pair.otFont, &opentype.FaceOptions{
			Size:    cf.size,
			DPI:     72,
			Hinting: font.HintingNone,
		})
		if err == nil {
			inst.faces[i] = face
		}
	}
	return inst
}

func (inst *ChainedFaceInstance) DrawString(dc *gg.Context, text string, x, y float64) {
	dotX := fixed.I(int(x))
	dotY := fixed.I(int(y))

	for _, r := range text {
		var selectedFace font.Face
		for i := range inst.sFonts {
			if inst.sFonts[i] == nil || inst.faces[i] == nil {
				continue
			}
			idx, err := inst.sFonts[i].GlyphIndex(&inst.buf, r)
			if err == nil && idx != 0 {
				selectedFace = inst.faces[i]
				break
			}
		}
		if selectedFace == nil && len(inst.faces) > 0 {
			selectedFace = inst.faces[len(inst.faces)-1]
		}
		if selectedFace == nil {
			continue
		}

		dc.SetFontFace(selectedFace)
		dc.DrawString(string(r), float64(dotX)/64.0, float64(dotY)/64.0)

		adv, ok := selectedFace.GlyphAdvance(r)
		if !ok {
			adv = fixed.I(int(inst.faces[0].Metrics().Ascent.Ceil() / 2))
		}
		dotX += adv
	}
}

func (inst *ChainedFaceInstance) MeasureString(text string) (width, height float64) {
	dotX := fixed.I(0)
	var maxAscent, maxDescent fixed.Int26_6

	for _, r := range text {
		var selectedFace font.Face
		for i := range inst.sFonts {
			if inst.sFonts[i] == nil || inst.faces[i] == nil {
				continue
			}
			idx, err := inst.sFonts[i].GlyphIndex(&inst.buf, r)
			if err == nil && idx != 0 {
				selectedFace = inst.faces[i]
				break
			}
		}
		if selectedFace == nil && len(inst.faces) > 0 {
			selectedFace = inst.faces[len(inst.faces)-1]
		}
		if selectedFace == nil {
			continue
		}

		m := selectedFace.Metrics()
		if m.Ascent > maxAscent {
			maxAscent = m.Ascent
		}
		if m.Descent > maxDescent {
			maxDescent = m.Descent
		}

		adv, ok := selectedFace.GlyphAdvance(r)
		if !ok {
			adv = fixed.I(int(inst.faces[0].Metrics().Ascent.Ceil() / 2))
		}
		dotX += adv
	}
	return float64(dotX) / 64.0, float64(maxAscent+maxDescent) / 64.0
}

func (inst *ChainedFaceInstance) TruncateWithEllipsis(text string, maxWidth float64) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	w, _ := inst.MeasureString(text)
	if w <= maxWidth {
		return text
	}

	ellipsis := "..."
	ellW, _ := inst.MeasureString(ellipsis)
	if ellW >= maxWidth {
		return ellipsis
	}

	runes := []rune(text)
	low := 0
	high := len(runes)
	best := 0

	for low <= high {
		mid := (low + high) / 2
		candidate := strings.TrimRight(string(runes[:mid]), " ") + ellipsis
		cw, _ := inst.MeasureString(candidate)
		if cw <= maxWidth {
			best = mid
			low = mid + 1
		} else {
			high = mid - 1
		}
	}

	if best == 0 {
		return ellipsis
	}
	return strings.TrimRight(string(runes[:best]), " ") + ellipsis
}

func (inst *ChainedFaceInstance) DrawStringAnchored(dc *gg.Context, text string, x, y, ax, ay float64) {
	w, h := inst.MeasureString(text)
	x -= ax * w
	y += (1.0 - ay) * (h / 2.0)
	inst.DrawString(dc, text, x, y)
}

func downloadAndBlurCover(coverURL string, targetW, targetH int) image.Image {
	if coverURL != "" {
		client := &http.Client{Timeout: 4 * time.Second}
		req, err := http.NewRequest("GET", coverURL, nil)
		if err == nil {
			req.Header.Set("User-Agent", "Mozilla/5.0")
			resp, err := client.Do(req)
			if err == nil && resp.StatusCode == 200 {
				defer resp.Body.Close()
				srcImg, _, err := image.Decode(resp.Body)
				if err == nil {
					// Aspect Fill & Center Crop
					srcB := srcImg.Bounds()
					srcW := srcB.Dx()
					srcH := srcB.Dy()

					scaleX := float64(targetW) / float64(srcW)
					scaleY := float64(targetH) / float64(srcH)
					scale := scaleX
					if scaleY > scale {
						scale = scaleY
					}

					newW := int(float64(srcW) * scale)
					newH := int(float64(srcH) * scale)

					gResize := gift.New(
						gift.Resize(newW, newH, gift.LinearResampling),
						gift.CropToSize(targetW, targetH, gift.CenterAnchor),
						gift.GaussianBlur(38),
						gift.Brightness(-42),
						gift.Contrast(-10),
					)
					dstImg := image.NewRGBA(gResize.Bounds(image.Rect(0, 0, targetW, targetH)))
					gResize.Draw(dstImg, srcImg)
					return dstImg
				}
			}
		}
	}

	// Fallback dark gradient
	dc := gg.NewContext(targetW, targetH)
	grad := gg.NewLinearGradient(0, 0, float64(targetW), float64(targetH))
	grad.AddColorStop(0, color.RGBA{34, 22, 52, 255})
	grad.AddColorStop(1, color.RGBA{16, 14, 28, 255})
	dc.SetFillStyle(grad)
	dc.DrawRectangle(0, 0, float64(targetW), float64(targetH))
	dc.Fill()
	return dc.Image()
}

func RenderNotFoundLyricsFrame(coverURL string, outPath string) error {
	targetW := 1600
	targetH := 640

	baseBg := downloadAndBlurCover(coverURL, targetW, targetH)
	dc := gg.NewContext(targetW, targetH)
	dc.DrawImage(baseBg, 0, 0)

	cfMain := newChainedFontFace(60)
	instMain := cfMain.NewInstance()

	dc.SetRGB255(255, 255, 255)
	instMain.DrawStringAnchored(dc, "가사를 찾을 수 없습니다.", float64(targetW)/2.0, float64(targetH)/2.0, 0.5, 0.5)

	if dir := filepath.Dir(outPath); dir != "" {
		_ = os.MkdirAll(dir, 0755)
	}
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return jpeg.Encode(f, dc.Image(), &jpeg.Options{Quality: 88})
}

func RenderLyricsFramesNative(coverURL string, durationMs int, lines []RenderLyricLine, outDir string) error {
	if len(lines) == 0 {
		return nil
	}
	_ = os.MkdirAll(outDir, 0755)

	targetW := 1600
	targetH := 640

	if durationMs <= 0 {
		durationMs = lines[len(lines)-1].TimeMs + 5000
	}

	// 1. Prepare Base Background (1-time Blur)
	t0 := time.Now()
	baseBg := downloadAndBlurCover(coverURL, targetW, targetH)
	bgElapsed := time.Since(t0)

	cfMain := newChainedFontFace(60)
	cfSub := newChainedFontFace(36)
	cfPhonetic := newChainedFontFace(32)
	cfDim := newChainedFontFace(36)
	cfDimSmall := newChainedFontFace(30)

	renderWorker := func(idx int) {
		dc := gg.NewContext(targetW, targetH)
		dc.DrawImage(baseBg, 0, 0)

		cur := lines[idx]
		curTime := cur.TimeMs

		// 1. Top progress bar (Violet/Purple)
		progRatio := float64(curTime) / float64(durationMs)
		if progRatio < 0 {
			progRatio = 0
		} else if progRatio > 1.0 {
			progRatio = 1.0
		}
		progW := progRatio * float64(targetW)
		dc.SetRGB255(175, 135, 245)
		dc.DrawRectangle(0, 0, progW, 9)
		dc.Fill()

		dc.SetRGBA255(55, 50, 68, 170)
		dc.DrawRectangle(progW, 0, float64(targetW)-progW, 9)
		dc.Fill()

		leftX := 85.0

		instMain := cfMain.NewInstance()
		instSub := cfSub.NewInstance()
		instPhonetic := cfPhonetic.NewInstance()
		instDim := cfDim.NewInstance()
		instDimSmall := cfDimSmall.NewInstance()

		// Determine vertical layout based on whether translation/phonetic exist
		hasTrans := cur.Trans != ""
		hasPhonetic := cur.Phonetic != ""

		var curY, prev1Y, prev2Y, next1Y, next2Y float64

		if !hasTrans && !hasPhonetic {
			// Case A: Perfectly centered single main line (before translation arrives)
			curY = 340.0
			prev1Y = 235.0
			prev2Y = 160.0
			next1Y = 435.0
			next2Y = 505.0
		} else if hasTrans && hasPhonetic {
			// Case C: Main + Translation + Phonetic (3 lines active - centered around 320px)
			curY = 280.0
			prev1Y = 192.0
			prev2Y = 128.0
			next1Y = 470.0
			next2Y = 535.0
		} else {
			// Case B & D: Main + Translation OR Main + Phonetic (2 lines active - centered around 320px)
			curY = 320.0
			prev1Y = 228.0
			prev2Y = 158.0
			next1Y = 465.0
			next2Y = 535.0
		}

		maxLineW := float64(targetW) - (leftX * 2) // 1430.0 px

		// 2. Previous Lines
		if idx >= 2 {
			dc.SetRGBA255(135, 135, 145, 130)
			p2Text := instDimSmall.TruncateWithEllipsis(lines[idx-2].Text, maxLineW)
			instDimSmall.DrawString(dc, p2Text, leftX, prev2Y)
		}
		if idx >= 1 {
			dc.SetRGBA255(165, 165, 175, 175)
			p1Text := instDim.TruncateWithEllipsis(lines[idx-1].Text, maxLineW)
			instDim.DrawString(dc, p1Text, leftX, prev1Y)
		}

		// 3. Active Line
		dc.SetRGB255(255, 255, 255)
		curMainText := instMain.TruncateWithEllipsis(cur.Text, maxLineW)
		instMain.DrawString(dc, curMainText, leftX, curY)

		if hasTrans && hasPhonetic {
			activeSubY := curY + 62.0
			dc.SetRGBA255(200, 200, 210, 235)
			curTransText := instSub.TruncateWithEllipsis(cur.Trans, maxLineW)
			instSub.DrawString(dc, curTransText, leftX, activeSubY)

			activeSubY += 48.0
			dc.SetRGB255(205, 170, 255)
			curPhoneticText := instPhonetic.TruncateWithEllipsis(cur.Phonetic, maxLineW)
			instPhonetic.DrawString(dc, curPhoneticText, leftX, activeSubY)
		} else if hasTrans {
			activeSubY := curY + 65.0
			dc.SetRGBA255(200, 200, 210, 235)
			curTransText := instSub.TruncateWithEllipsis(cur.Trans, maxLineW)
			instSub.DrawString(dc, curTransText, leftX, activeSubY)
		} else if hasPhonetic {
			activeSubY := curY + 65.0
			dc.SetRGB255(205, 170, 255)
			curPhoneticText := instPhonetic.TruncateWithEllipsis(cur.Phonetic, maxLineW)
			instPhonetic.DrawString(dc, curPhoneticText, leftX, activeSubY)
		}

		// 4. Next Lines
		if idx+1 < len(lines) {
			dc.SetRGBA255(165, 165, 175, 175)
			n1Text := instDim.TruncateWithEllipsis(lines[idx+1].Text, maxLineW)
			instDim.DrawString(dc, n1Text, leftX, next1Y)
		}
		if idx+2 < len(lines) {
			dc.SetRGBA255(135, 135, 145, 130)
			n2Text := instDimSmall.TruncateWithEllipsis(lines[idx+2].Text, maxLineW)
			instDimSmall.DrawString(dc, n2Text, leftX, next2Y)
		}

		framePath := filepath.Join(outDir, fmt.Sprintf("frame_%03d.jpg", idx))
		f, err := os.Create(framePath)
		if err == nil {
			_ = jpeg.Encode(f, dc.Image(), &jpeg.Options{Quality: 86})
			_ = f.Close()
		}

		// Save current live frame
		if idx == 0 || idx == len(lines)-1 {
			curLink := "/tmp/lyrics_current.jpg"
			if cf, err := os.Create(curLink); err == nil {
				_ = jpeg.Encode(cf, dc.Image(), &jpeg.Options{Quality: 86})
				_ = cf.Close()
			}
		}
	}

	tBatch := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < len(lines); i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			renderWorker(idx)
		}(i)
	}
	wg.Wait()
	batchElapsed := time.Since(tBatch)

	slog.Info("Completed Pure Go lyrics rendering",
		"frames", len(lines),
		"bg_ms", bgElapsed.Milliseconds(),
		"batch_ms", batchElapsed.Milliseconds(),
	)
	return nil
}
