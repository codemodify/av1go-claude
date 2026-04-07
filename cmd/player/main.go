// Command player is a GUI video player that decodes AV1 video using the
// pure Go av1go decoder and displays frames in a window.
//
// Decodes all frames from an MP4 file and provides playback controls:
//
//	Space       - play/pause toggle
//	Right arrow - next frame (when paused)
//	Left arrow  - previous frame (when paused)
//	Home        - first frame
//	End         - last frame
//	F           - toggle fullscreen
//	Escape / Q  - quit
//
// Usage:
//
//	go run ./cmd/player <file.mp4>
package main

import (
	"av1go/decoder"
	"av1go/mp4"
	"av1go/obu"
	"fmt"
	"image"
	"image/color"
	"log"
	"os"
	"path/filepath"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

const (
	targetFPS       = 25
	progressBarH    = 4  // pixels tall for the progress bar
	progressBarPad  = 0  // no padding from bottom edge
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <file.mp4>\n", os.Args[0])
		os.Exit(1)
	}
	path := os.Args[1]

	frames, frameW, frameH := decodeAllFrames(path)
	if len(frames) == 0 {
		log.Fatalf("no frames decoded from %s", path)
	}

	title := fmt.Sprintf("%s - %dx%d - av1go player",
		filepath.Base(path), frameW, frameH)

	paused := true
	if len(frames) <= 1 {
		// Single frame: always paused, nothing to play.
		paused = true
	} else {
		// Multiple frames: start playing.
		paused = false
	}

	game := &playerGame{
		frames:     frames,
		frameW:     frameW,
		frameH:     frameH,
		title:      title,
		paused:     paused,
		current:    0,
		tickAccum:  0,
		fullscreen: false,
	}

	ebiten.SetWindowSize(frameW, frameH)
	ebiten.SetWindowTitle(title)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)

	if err := ebiten.RunGame(game); err != nil {
		log.Fatal(err)
	}
}

// playerGame implements ebiten.Game for multi-frame AV1 playback.
type playerGame struct {
	frames  []*ebiten.Image
	frameW  int
	frameH  int
	title   string
	current int  // current frame index
	paused  bool

	// tickAccum counts Update() calls; when it reaches the threshold we
	// advance a frame. Ebiten runs Update at 60 TPS by default, so we
	// advance every (60/25) ~2.4 ticks to approximate 25fps.
	tickAccum float64

	fullscreen bool
}

// ticksPerFrame is how many Update() ticks correspond to one video frame
// at the target frame rate. Ebiten default TPS is 60.
func ticksPerFrame() float64 {
	return float64(ebiten.TPS()) / float64(targetFPS)
}

func (g *playerGame) Update() error {
	// --- Keyboard input ---

	// Quit
	if inpututil.IsKeyJustPressed(ebiten.KeyEscape) || inpututil.IsKeyJustPressed(ebiten.KeyQ) {
		return ebiten.Termination
	}

	// Fullscreen toggle
	if inpututil.IsKeyJustPressed(ebiten.KeyF) {
		g.fullscreen = !g.fullscreen
		ebiten.SetFullscreen(g.fullscreen)
	}

	total := len(g.frames)

	// Play/pause toggle (only meaningful with >1 frame)
	if inpututil.IsKeyJustPressed(ebiten.KeySpace) && total > 1 {
		g.paused = !g.paused
		if !g.paused {
			g.tickAccum = 0
		}
	}

	// Frame stepping when paused
	if g.paused {
		if inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) {
			if g.current < total-1 {
				g.current++
			}
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) {
			if g.current > 0 {
				g.current--
			}
		}
	}

	// Home / End
	if inpututil.IsKeyJustPressed(ebiten.KeyHome) {
		g.current = 0
		g.tickAccum = 0
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyEnd) {
		g.current = total - 1
		g.paused = true
	}

	// --- Mouse click on progress bar to seek ---
	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) && total > 1 {
		mx, my := ebiten.CursorPosition()
		sw, sh := ebiten.WindowSize()
		if my >= sh-progressBarH-8 { // generous click area
			frac := float64(mx) / float64(sw)
			if frac < 0 {
				frac = 0
			}
			if frac > 1 {
				frac = 1
			}
			g.current = int(frac * float64(total-1))
			g.tickAccum = 0
		}
	}

	// --- Playback advance ---
	if !g.paused && total > 1 {
		g.tickAccum++
		if g.tickAccum >= ticksPerFrame() {
			g.tickAccum -= ticksPerFrame()
			g.current++
			if g.current >= total {
				// Loop back to start.
				g.current = 0
			}
		}
	}

	return nil
}

func (g *playerGame) Draw(screen *ebiten.Image) {
	sw, sh := screen.Bounds().Dx(), screen.Bounds().Dy()

	// Draw the current frame scaled to fit.
	frameImg := g.frames[g.current]
	iw, ih := frameImg.Bounds().Dx(), frameImg.Bounds().Dy()

	scaleX := float64(sw) / float64(iw)
	scaleY := float64(sh) / float64(ih)
	scale := scaleX
	if scaleY < scaleX {
		scale = scaleY
	}

	opts := &ebiten.DrawImageOptions{}
	offsetX := (float64(sw) - float64(iw)*scale) / 2
	offsetY := (float64(sh) - float64(ih)*scale) / 2
	opts.GeoM.Scale(scale, scale)
	opts.GeoM.Translate(offsetX, offsetY)
	screen.DrawImage(frameImg, opts)

	// --- On-screen display ---
	total := len(g.frames)
	status := ""
	if g.paused {
		status = " [PAUSED]"
	} else {
		status = " [PLAYING]"
	}
	info := fmt.Sprintf("Frame %d/%d%s", g.current+1, total, status)
	ebitenutil.DebugPrint(screen, info)

	// --- Progress bar ---
	if total > 1 {
		g.drawProgressBar(screen, sw, sh)
	}
}

// pixel is a reusable 1x1 white image used for drawing rectangles.
var pixel *ebiten.Image

func init() {
	pixel = ebiten.NewImage(1, 1)
	pixel.Fill(color.White)
}

// drawRect draws a filled rectangle on dst using the shared 1x1 pixel image.
func drawRect(dst *ebiten.Image, x, y, w, h int, c color.RGBA) {
	if w <= 0 || h <= 0 {
		return
	}
	opts := &ebiten.DrawImageOptions{}
	opts.GeoM.Scale(float64(w), float64(h))
	opts.GeoM.Translate(float64(x), float64(y))
	opts.ColorScale.ScaleWithColor(c)
	dst.DrawImage(pixel, opts)
}

func (g *playerGame) drawProgressBar(screen *ebiten.Image, sw, sh int) {
	total := len(g.frames)
	barY := sh - progressBarH - progressBarPad

	// Background bar (dark gray).
	drawRect(screen, 0, barY, sw, progressBarH, color.RGBA{R: 60, G: 60, B: 60, A: 180})

	// Filled portion (light gray / white).
	filled := 0
	if total > 1 {
		filled = (g.current * sw) / (total - 1)
	}
	if filled > sw {
		filled = sw
	}
	drawRect(screen, 0, barY, filled, progressBarH, color.RGBA{R: 220, G: 220, B: 220, A: 220})
}

func (g *playerGame) Layout(outsideWidth, outsideHeight int) (int, int) {
	return outsideWidth, outsideHeight
}

// decodeAllFrames opens an MP4 file and decodes all AV1 frames.
// Returns the slice of ebiten images, plus the video dimensions.
// On decode errors for individual frames, the last successful frame is reused.
func decodeAllFrames(path string) ([]*ebiten.Image, int, int) {
	f, err := mp4.Open(path)
	if err != nil {
		log.Fatalf("open MP4: %v", err)
	}

	vt := f.VideoTrack()
	if vt == nil {
		log.Fatalf("no AV1 video track found")
	}

	dec := decoder.New()

	// Feed config OBUs first.
	if len(vt.AV1ConfigOBUs) > 0 {
		configUnits, err := obu.ParseUnits(vt.AV1ConfigOBUs)
		if err != nil {
			log.Fatalf("parse configOBUs: %v", err)
		}
		if _, err := dec.DecodeOBUs(configUnits); err != nil {
			log.Fatalf("decode configOBUs: %v", err)
		}
	}

	samples := vt.Samples()
	totalSamples := len(samples)
	fmt.Printf("Decoding %d samples...\n", totalSamples)

	var frames []*ebiten.Image
	var lastRGBA *image.RGBA
	var frameW, frameH int

	for i := range samples {
		data, err := f.ReadSample(vt, i)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sample %d/%d: read error: %v (skipping)\n", i+1, totalSamples, err)
			if lastRGBA != nil {
				frames = append(frames, ebiten.NewImageFromImage(lastRGBA))
			}
			continue
		}

		units, err := obu.ParseUnits(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sample %d/%d: parse error: %v (skipping)\n", i+1, totalSamples, err)
			if lastRGBA != nil {
				frames = append(frames, ebiten.NewImageFromImage(lastRGBA))
			}
			continue
		}

		fb, err := dec.DecodeOBUs(units)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sample %d/%d: decode error: %v (using last frame)\n", i+1, totalSamples, err)
			if lastRGBA != nil {
				frames = append(frames, ebiten.NewImageFromImage(lastRGBA))
			}
			continue
		}

		if fb == nil {
			// Some samples may not produce a frame (e.g., temporal delimiter only).
			continue
		}

		if frameW == 0 {
			frameW = fb.Width
			frameH = fb.Height
		}

		rgba := yuvToRGBA(fb)
		lastRGBA = rgba
		frames = append(frames, ebiten.NewImageFromImage(rgba))

		fmt.Printf("\rDecoded frame %d/%d", len(frames), totalSamples)
	}

	fmt.Println() // newline after progress
	fmt.Printf("Total frames decoded: %d\n", len(frames))

	return frames, frameW, frameH
}

// yuvToRGBA converts a YUV 4:2:0 FrameBuffer to an RGBA image using
// BT.601 coefficients (matching the existing PPM writer in the decoder package).
func yuvToRGBA(fb *decoder.FrameBuffer) *image.RGBA {
	w, h := fb.Width, fb.Height
	img := image.NewRGBA(image.Rect(0, 0, w, h))

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			yVal := int(fb.Y[y*fb.StrideY+x])

			// Chroma is half resolution in both dimensions (4:2:0).
			cx := x >> 1
			cy := y >> 1
			uVal := int(fb.U[cy*fb.StrideU+cx]) - 128
			vVal := int(fb.V[cy*fb.StrideV+cx]) - 128

			// BT.601 YUV to RGB conversion (studio range).
			// These are the same fixed-point coefficients used in decoder.WritePPM.
			r := yVal + ((91881*vVal + 32768) >> 16)
			g := yVal - ((22554*uVal + 46802*vVal + 32768) >> 16)
			b := yVal + ((116130*uVal + 32768) >> 16)

			img.SetRGBA(x, y, color.RGBA{
				R: clipByte(r),
				G: clipByte(g),
				B: clipByte(b),
				A: 255,
			})
		}
	}

	return img
}

// clipByte clamps an integer to the [0, 255] range.
func clipByte(v int) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}
