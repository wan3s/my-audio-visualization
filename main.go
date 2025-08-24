package main

import (
	"errors"
	"fmt"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/faiface/beep"
	"github.com/faiface/beep/flac"
	"github.com/faiface/beep/mp3"
	"github.com/faiface/beep/speaker"
	"github.com/faiface/beep/wav"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"github.com/ncruces/zenity"
)

const (
	windowWidth  = 1024
	windowHeight = 512

	visualRingSize  = 8192
	smoothingFactor = 0.6

	// Button dimensions
	buttonWidth  = 120
	buttonHeight = 40
	buttonX      = 20
	buttonY      = 50

	// Visualization parameters
	circleCount     = 8
	waveCount       = 12
	particleCount   = 50
	maxRadius       = 200
	rotationSpeed   = 0.02
	colorShiftSpeed = 0.01
)

// visualTap wraps a beep.Streamer and records the last N samples into a ring buffer
// so the renderer can draw a visualization from recently played audio.
type visualTap struct {
	Source    beep.Streamer
	buffer    [][2]float64
	nextIndex int
	mu        sync.RWMutex
}

func newVisualTap(src beep.Streamer, ringSize int) *visualTap {
	return &visualTap{
		Source: src,
		buffer: make([][2]float64, ringSize),
	}
}

func (t *visualTap) Stream(samples [][2]float64) (int, bool) {
	n, ok := t.Source.Stream(samples)
	if n > 0 {
		t.mu.Lock()
		for i := 0; i < n; i++ {
			t.buffer[t.nextIndex] = samples[i]
			t.nextIndex++
			if t.nextIndex >= len(t.buffer) {
				t.nextIndex = 0
			}
		}
		t.mu.Unlock()
	}
	return n, ok
}

func (t *visualTap) Err() error { return t.Source.Err() }

// snapshot returns up to last n samples (stereo) from the ring buffer (most recent last).
func (t *visualTap) snapshot(n int) [][2]float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if n > len(t.buffer) {
		n = len(t.buffer)
	}
	out := make([][2]float64, 0, n)
	// Walk backwards from nextIndex - 1
	idx := t.nextIndex - 1
	if idx < 0 {
		idx = len(t.buffer) - 1
	}
	for i := 0; i < n; i++ {
		out = append(out, t.buffer[idx])
		idx--
		if idx < 0 {
			idx = len(t.buffer) - 1
		}
	}
	// reverse to chronological order
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

type game struct {
	// audio
	currentFile *os.File
	streamer    beep.StreamSeekCloser
	format      beep.Format
	ctrl        *beep.Ctrl
	tap         *visualTap

	// viz
	audioData  []float64
	time       float64
	rotation   float64
	colorPhase float64

	// progress bar
	progressBarHovered   bool
	progressBarDragging  bool
	progressBarDragStart float64
	audioDuration        time.Duration
	audioPosition        time.Duration
	lastSeekTime         time.Time

	// input edge detection
	prevKey map[ebiten.Key]bool

	// button state
	buttonHovered bool
	buttonPressed bool

	// state
	paused   bool
	initDone bool
	lastErr  error
}

func NewGame() *game {
	return &game{
		prevKey: map[ebiten.Key]bool{},
	}
}

func (g *game) Update() error {

	justPressed := func(k ebiten.Key) bool {
		pressed := ebiten.IsKeyPressed(k)
		jp := pressed && !g.prevKey[k]
		g.prevKey[k] = pressed
		return jp
	}

	// Handle button interactions
	mouseX, mouseY := ebiten.CursorPosition()
	g.buttonHovered = mouseX >= buttonX && mouseX <= buttonX+buttonWidth &&
		mouseY >= buttonY && mouseY <= buttonY+buttonHeight

	// Button click detection
	if g.buttonHovered && inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
		g.buttonPressed = true
	}
	if inpututil.IsMouseButtonJustReleased(ebiten.MouseButtonLeft) {
		if g.buttonPressed && g.buttonHovered {
			// Button was clicked
			if err := g.openAndPlayFileDialog(); err != nil {
				g.lastErr = err
			}
		}
		g.buttonPressed = false
	}

	// Progress bar interactions
	barHeight := 30
	barY := windowHeight - 120
	barWidth := windowWidth - 40
	barX := 20

	g.progressBarHovered = mouseX >= barX && mouseX <= barX+barWidth &&
		mouseY >= barY && mouseY <= barY+barHeight

	// Progress bar click and drag (only if audio is loaded)
	if g.progressBarHovered && g.streamer != nil && g.audioDuration > 0 {
		if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
			g.progressBarDragging = true
			g.progressBarDragStart = float64(mouseX-barX) / float64(barWidth)
			// Only seek on initial click, not on drag start
			g.seekToPosition(g.progressBarDragStart)
		}

		if inpututil.IsMouseButtonJustReleased(ebiten.MouseButtonLeft) {
			if g.progressBarDragging {
				g.progressBarDragging = false
			}
		}

		// Handle dragging with debouncing to avoid too many seek calls
		if g.progressBarDragging {
			mouseProgress := float64(mouseX-barX) / float64(barWidth)
			if mouseProgress < 0 {
				mouseProgress = 0
			}
			if mouseProgress > 1 {
				mouseProgress = 1
			}

			// Only seek if the position changed significantly (avoid micro-seeks)
			currentProgress := float64(g.audioPosition) / float64(g.audioDuration)
			if math.Abs(mouseProgress-currentProgress) > 0.01 { // 1% threshold
				g.seekToPosition(mouseProgress)
			}
		}
	}

	if justPressed(ebiten.KeySpace) {
		g.togglePause()
	}
	if justPressed(ebiten.KeyEscape) || justPressed(ebiten.KeyQ) {
		return ebiten.Termination
	}

	// Update visualization
	g.time += 1.0 / 60.0 // Assuming 60 FPS
	g.rotation += rotationSpeed
	g.colorPhase += colorShiftSpeed
	g.updateAudioData()
	g.updateAudioPosition()

	return nil
}

func (g *game) Draw(screen *ebiten.Image) {
	// Clear background with gradient
	g.drawBackground(screen)

	// Draw button
	g.drawButton(screen)

	// Draw complex visualization
	g.drawComplexVisualization(screen)

	// Draw progress bar
	g.drawProgressBar(screen)

	// Draw audio bar
	g.drawAudioBar(screen)

	// Draw help
	status := ""
	if g.ctrl == nil {
		status = "Click the button above to open an audio file"
	} else if g.paused {
		status = "Paused - Space to play, click button to open another"
	} else {
		status = "Playing - Space to pause, click button to open another"
	}
	if g.lastErr != nil {
		status += " | Error: " + g.lastErr.Error()
	}
	ebitenutil.DebugPrintAt(screen, status, 12, 12)
}

func (g *game) drawBackground(screen *ebiten.Image) {
	// Create a dynamic gradient background
	for y := 0; y < windowHeight; y++ {
		ratio := float64(y) / float64(windowHeight)
		r := uint8(10 + 20*math.Sin(g.time*0.5+ratio*math.Pi))
		g_val := uint8(12 + 15*math.Cos(g.time*0.3+ratio*math.Pi))
		b := uint8(20 + 25*math.Sin(g.time*0.7+ratio*math.Pi))
		ebitenutil.DrawLine(screen, 0, float64(y), float64(windowWidth), float64(y), color.RGBA{R: r, G: g_val, B: b, A: 255})
	}
}

func (g *game) drawComplexVisualization(screen *ebiten.Image) {
	if len(g.audioData) == 0 {
		return
	}

	centerX := float64(windowWidth) / 2
	centerY := float64(windowHeight) / 2

	// Draw animated circles
	g.drawAnimatedCircles(screen, centerX, centerY)

	// Draw wave patterns
	g.drawWavePatterns(screen, centerX, centerY)

	// Draw particle effects
	g.drawParticleEffects(screen, centerX, centerY)

	// Draw energy rings
	g.drawEnergyRings(screen, centerX, centerY)
}

func (g *game) drawAnimatedCircles(screen *ebiten.Image, centerX, centerY float64) {
	for i := 0; i < circleCount; i++ {
		angle := float64(i) * (2 * math.Pi / float64(circleCount))
		radius := 30 + float64(i)*15 + g.audioData[i%len(g.audioData)]*100

		x := centerX + math.Cos(angle+g.rotation)*radius
		y := centerY + math.Sin(angle+g.rotation)*radius

		// Dynamic color based on audio and time
		hue := (g.colorPhase + float64(i)*0.1) * 360
		r, g_val, b := hsvToRgb(hue, 0.8, 0.9)

		// Draw circle with varying opacity
		opacity := uint8(150 + 105*g.audioData[i%len(g.audioData)])
		circleColor := color.RGBA{R: r, G: g_val, B: b, A: opacity}

		circleRadius := 8 + g.audioData[i%len(g.audioData)]*20
		vector.DrawFilledCircle(screen, float32(x), float32(y), float32(circleRadius), circleColor, false)
	}
}

func (g *game) drawWavePatterns(screen *ebiten.Image, centerX, centerY float64) {
	for i := 0; i < waveCount; i++ {
		angle := float64(i) * (2 * math.Pi / float64(waveCount))
		waveRadius := 80 + g.audioData[i%len(g.audioData)]*150

		// Create wave effect
		for j := 0; j < 360; j += 5 {
			waveAngle := float64(j) * math.Pi / 180
			waveOffset := math.Sin(waveAngle*3+g.time*2) * 10
			waveRadiusOffset := waveRadius + waveOffset + g.audioData[i%len(g.audioData)]*50

			x1 := centerX + math.Cos(angle)*waveRadiusOffset
			y1 := centerY + math.Sin(angle)*waveRadiusOffset

			nextAngle := float64(j+5) * math.Pi / 180
			nextOffset := math.Sin(nextAngle*3+g.time*2) * 10
			nextRadiusOffset := waveRadius + nextOffset + g.audioData[i%len(g.audioData)]*50

			x2 := centerX + math.Cos(angle)*nextRadiusOffset
			y2 := centerY + math.Sin(angle)*nextRadiusOffset

			// Color based on wave position and audio
			hue := (g.colorPhase + float64(i)*0.05 + float64(j)*0.01) * 360
			r, g_val, b := hsvToRgb(hue, 0.7, 0.8)

			opacity := uint8(100 + 155*g.audioData[i%len(g.audioData)])
			waveColor := color.RGBA{R: r, G: g_val, B: b, A: opacity}

			vector.StrokeLine(screen, float32(x1), float32(y1), float32(x2), float32(y2), 2, waveColor, false)
		}
	}
}

func (g *game) drawParticleEffects(screen *ebiten.Image, centerX, centerY float64) {
	for i := 0; i < particleCount; i++ {
		// Particle position based on audio and time
		angle := g.time*0.5 + float64(i)*0.1
		radius := 20 + g.audioData[i%len(g.audioData)]*300

		x := centerX + math.Cos(angle)*radius
		y := centerY + math.Sin(angle)*radius

		// Particle size and color
		size := 2 + g.audioData[i%len(g.audioData)]*8
		hue := (g.colorPhase + float64(i)*0.02) * 360
		r, g_val, b := hsvToRgb(hue, 1.0, 1.0)

		opacity := uint8(200 + 55*g.audioData[i%len(g.audioData)])
		particleColor := color.RGBA{R: r, G: g_val, B: b, A: opacity}

		vector.DrawFilledCircle(screen, float32(x), float32(y), float32(size), particleColor, false)
	}
}

func (g *game) drawEnergyRings(screen *ebiten.Image, centerX, centerY float64) {
	for i := 0; i < 5; i++ {
		ringRadius := float64(40+i*30) + g.audioData[i%len(g.audioData)]*100

		// Draw ring segments
		segments := 24
		for j := 0; j < segments; j++ {
			startAngle := float64(j) * (2 * math.Pi / float64(segments))
			endAngle := float64(j+1) * (2 * math.Pi / float64(segments))

			// Skip some segments based on audio intensity
			if g.audioData[i%len(g.audioData)] < 0.1 {
				continue
			}

			x1 := centerX + math.Cos(startAngle)*ringRadius
			y1 := centerY + math.Sin(startAngle)*ringRadius
			x2 := centerX + math.Cos(endAngle)*ringRadius
			y2 := centerY + math.Sin(endAngle)*ringRadius

			// Color based on ring and segment
			hue := (g.colorPhase + float64(i)*0.2 + float64(j)*0.1) * 360
			r, g_val, b := hsvToRgb(hue, 0.9, 0.8)

			opacity := uint8(120 + 135*g.audioData[i%len(g.audioData)])
			ringColor := color.RGBA{R: r, G: g_val, B: b, A: opacity}

			strokeWidth := 3 + g.audioData[i%len(g.audioData)]*8
			vector.StrokeLine(screen, float32(x1), float32(y1), float32(x2), float32(y2), float32(strokeWidth), ringColor, false)
		}
	}
}

// hsvToRgb converts HSV to RGB (hue: 0-360, saturation: 0-1, value: 0-1)
func hsvToRgb(h, s, v float64) (uint8, uint8, uint8) {
	h = math.Mod(h, 360)
	c := v * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := v - c

	var r, g, b float64
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}

	return uint8((r + m) * 255), uint8((g + m) * 255), uint8((b + m) * 255)
}

func (g *game) drawAudioBar(screen *ebiten.Image) {
	if len(g.audioData) == 0 {
		return
	}

	// Audio bar parameters
	barHeight := 60
	barY := windowHeight - barHeight - 20
	barWidth := windowWidth - 40
	barX := 20
	segmentWidth := float64(barWidth) / 64.0 // 64 frequency bands

	// Draw background for audio bar
	vector.DrawFilledRect(screen, float32(barX), float32(barY), float32(barWidth), float32(barHeight), color.RGBA{R: 20, G: 25, B: 35, A: 200}, false)
	vector.StrokeRect(screen, float32(barX), float32(barY), float32(barWidth), float32(barHeight), 2, color.RGBA{R: 60, G: 70, B: 90, A: 255}, false)

	// Draw frequency segments
	for i := 0; i < 64; i++ {
		if i >= len(g.audioData) {
			break
		}

		// Calculate segment position and height
		segmentX := float64(barX) + float64(i)*segmentWidth
		segmentHeight := g.audioData[i] * float64(barHeight-10)

		// Ensure minimum height for visibility
		if segmentHeight < 2 {
			segmentHeight = 2
		}

		// Color based on frequency and intensity
		freqRatio := float64(i) / 64.0
		hue := (g.colorPhase + freqRatio*180) * 360
		r, g_val, b := hsvToRgb(hue, 0.8, 0.9)

		// Opacity based on audio intensity
		opacity := uint8(100 + 155*g.audioData[i])
		segmentColor := color.RGBA{R: r, G: g_val, B: b, A: opacity}

		// Draw segment
		segmentY := float64(barY) + float64(barHeight) - segmentHeight
		vector.DrawFilledRect(screen, float32(segmentX), float32(segmentY), float32(segmentWidth-1), float32(segmentHeight), segmentColor, false)

		// Add highlight effect for stronger frequencies
		if g.audioData[i] > 0.3 {
			highlightColor := color.RGBA{R: 255, G: 255, B: 255, A: uint8(100 * g.audioData[i])}
			vector.StrokeRect(screen, float32(segmentX), float32(segmentY), float32(segmentWidth-1), float32(segmentHeight), 1, highlightColor, false)
		}
	}

	// Draw center line indicator
	centerY := float64(barY) + float64(barHeight)/2
	vector.StrokeLine(screen, float32(barX), float32(centerY), float32(barX+barWidth), float32(centerY), 1, color.RGBA{R: 100, G: 110, B: 130, A: 100}, false)

	// Draw frequency labels
	ebitenutil.DebugPrintAt(screen, "Low", int(barX), int(barY-15))
	ebitenutil.DebugPrintAt(screen, "High", int(barX+barWidth-25), int(barY-15))
}

func (g *game) drawButton(screen *ebiten.Image) {
	// Button background
	var bgColor color.Color
	if g.buttonPressed {
		bgColor = color.RGBA{R: 60, G: 80, B: 120, A: 255} // Pressed
	} else if g.buttonHovered {
		bgColor = color.RGBA{R: 80, G: 100, B: 140, A: 255} // Hovered
	} else {
		bgColor = color.RGBA{R: 100, G: 120, B: 160, A: 255} // Normal
	}

	// Draw filled rectangle background
	vector.DrawFilledRect(screen, float32(buttonX), float32(buttonY), float32(buttonWidth), float32(buttonHeight), bgColor, false)

	// Button border
	borderColor := color.RGBA{R: 150, G: 170, B: 200, A: 255}
	vector.StrokeRect(screen, float32(buttonX), float32(buttonY), float32(buttonWidth), float32(buttonHeight), 2, borderColor, false)

	// Button text
	text := "Open File"
	textWidth := len(text) * 8 // Approximate character width
	textX := buttonX + (buttonWidth-textWidth)/2
	textY := buttonY + (buttonHeight+8)/2
	ebitenutil.DebugPrintAt(screen, text, textX, textY)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func (g *game) Layout(outsideWidth, outsideHeight int) (int, int) {
	return windowWidth, windowHeight
}

func (g *game) togglePause() {
	if g.ctrl == nil {
		return
	}
	speaker.Lock()
	g.paused = !g.paused
	g.ctrl.Paused = g.paused
	speaker.Unlock()
}

func (g *game) updateAudioData() {
	if g.tap == nil {
		return
	}

	// Get audio samples
	samples := g.tap.snapshot(2048)
	if len(samples) == 0 {
		return
	}

	// Process audio data into frequency bands
	nBands := 64
	if len(g.audioData) != nBands {
		g.audioData = make([]float64, nBands)
	}

	segmentSize := int(math.Max(1, float64(len(samples))/float64(nBands)))
	for i := 0; i < nBands; i++ {
		start := i * segmentSize
		end := start + segmentSize
		if start >= len(samples) {
			break
		}
		if end > len(samples) {
			end = len(samples)
		}

		var sumSquares float64
		for s := start; s < end; s++ {
			mono := (samples[s][0] + samples[s][1]) * 0.5
			sumSquares += mono * mono
		}

		rms := math.Sqrt(sumSquares / float64(end-start))
		mag := math.Pow(rms, 0.3) // More aggressive compression for visual effect

		// Smooth with previous value
		g.audioData[i] = smoothingFactor*g.audioData[i] + (1-smoothingFactor)*mag
	}
}

func (g *game) updateAudioPosition() {
	if g.streamer == nil || g.paused {
		return
	}

	// Simple time-based position tracking
	g.audioPosition += time.Second / 60 // Assuming 60 FPS
	if g.audioPosition > g.audioDuration {
		g.audioPosition = g.audioDuration
	}
}

func (g *game) seekToPosition(pos float64) {
	if g.streamer == nil {
		return
	}

	// Add cooldown to prevent too frequent seeking
	if time.Since(g.lastSeekTime) < 50*time.Millisecond {
		return
	}

	// Clamp position to valid range
	if pos < 0 {
		pos = 0
	}
	if pos > 1 {
		pos = 1
	}

	// Calculate seek position in samples
	seekPos := int(pos * float64(g.audioDuration) * float64(g.format.SampleRate) / float64(time.Second))

	// Ensure seek position is within valid bounds
	if seekPos < 0 {
		seekPos = 0
	}

	// Get the maximum valid position from the streamer
	maxPos := g.streamer.Len()
	if seekPos >= maxPos {
		seekPos = maxPos - 1
	}

	// Perform the seek
	err := g.streamer.Seek(seekPos)
	if err != nil {
		g.lastErr = err
		return
	}

	// Update the audio position and seek time
	g.audioPosition = time.Duration(seekPos) * time.Second / time.Duration(g.format.SampleRate.N(time.Second))
	g.lastSeekTime = time.Now()
}

func (g *game) stopCurrent() {
	fmt.Println("stopCurrent 1")
	speaker.Lock()
	fmt.Println("stopCurrent 2")
	speaker.Clear()
	fmt.Println("stopCurrent 3")
	speaker.Unlock()
	fmt.Println("stopCurrent 4")
	if g.streamer != nil {
		_ = g.streamer.Close()
		g.streamer = nil
	}
	if g.currentFile != nil {
		_ = g.currentFile.Close()
		g.currentFile = nil
	}
}

func (g *game) openAndPlayFileDialog() error {
	filename, err := zenity.SelectFile(
		zenity.Title("Open Audio File"),
		zenity.FileFilters{{
			Name:     "Audio",
			Patterns: []string{"*.wav", "*.mp3", "*.flac"},
		}},
	)
	if err != nil {
		if errors.Is(err, zenity.ErrCanceled) {
			return nil
		}
		return err
	}

	fmt.Printf("Succefully choosed file %v\n", filename)
	return g.loadAndPlay(filename)
}

func (g *game) loadAndPlay(path string) error {
	// Stop and close previous if any
	// g.stopCurrent()

	f, err := os.Open(path)
	if err != nil {
		return err
	}

	// Decode based on extension
	ext := filepath.Ext(path)
	var (
		streamer beep.StreamSeekCloser
		format   beep.Format
	)
	switch ext {
	case ".wav", ".WAV":
		streamer, format, err = wav.Decode(f)
	case ".mp3", ".MP3":
		streamer, format, err = mp3.Decode(f)
	case ".flac", ".FLAC":
		streamer, format, err = flac.Decode(f)
	default:
		_ = f.Close()
		return errors.New("unsupported file type: " + ext)
	}
	if err != nil {
		_ = f.Close()
		return err
	}

	fmt.Printf("Succefully loaded file %v\n", path)

	// Prepare audio chain: streamer -> tap -> ctrl
	t := newVisualTap(streamer, visualRingSize)
	ctrl := &beep.Ctrl{Streamer: t, Paused: false}

	// (Re)initialize speaker if needed
	bufferSize := format.SampleRate.N(time.Second / 20)
	if !g.initDone {
		if err := speaker.Init(format.SampleRate, bufferSize); err != nil {
			_ = streamer.Close()
			_ = f.Close()
			return err
		}
		g.initDone = true
	} else if g.format.SampleRate != format.SampleRate {
		// Re-init when sample rate changes
		speaker.Lock()
		speaker.Clear()
		if err := speaker.Init(format.SampleRate, bufferSize); err != nil {
			speaker.Unlock()
			_ = streamer.Close()
			_ = f.Close()
			return err
		}
		speaker.Unlock()
	} else {
		// Stop any previous playback
		speaker.Lock()
		speaker.Clear()
		speaker.Unlock()
	}

	g.currentFile = f
	g.streamer = streamer
	g.format = format
	g.ctrl = ctrl
	g.tap = t
	g.paused = false

	// Initialize progress bar
	g.audioDuration = time.Duration(streamer.Len()) * time.Second / time.Duration(format.SampleRate.N(time.Second))
	g.audioPosition = 0

	// Start playing
	speaker.Play(beep.Seq(ctrl, beep.Callback(func() {
		// On end: close resources
		_ = streamer.Close()
		_ = f.Close()
		g.streamer = nil
		g.currentFile = nil
		g.audioDuration = 0
		g.audioPosition = 0
	})))

	return nil
}

func (g *game) drawProgressBar(screen *ebiten.Image) {
	if g.streamer == nil || g.audioDuration == 0 {
		return
	}

	// Progress bar parameters
	barHeight := 30
	barY := windowHeight - 120 // Above the audio bar
	barWidth := windowWidth - 40
	barX := 20

	// Calculate progress
	progress := 0.0
	if g.audioDuration > 0 {
		progress = float64(g.audioPosition) / float64(g.audioDuration)
	}

	// Draw background
	vector.DrawFilledRect(screen, float32(barX), float32(barY), float32(barWidth), float32(barHeight), color.RGBA{R: 25, G: 30, B: 40, A: 200}, false)
	vector.StrokeRect(screen, float32(barX), float32(barY), float32(barWidth), float32(barHeight), 2, color.RGBA{R: 70, G: 80, B: 100, A: 255}, false)

	// Draw progress fill
	if progress > 0 {
		fillWidth := progress * float64(barWidth)
		// Gradient color based on progress
		hue := (g.colorPhase + progress*180) * 360
		r, g_val, b := hsvToRgb(hue, 0.8, 0.9)
		progressColor := color.RGBA{R: r, G: g_val, B: b, A: 180}

		vector.DrawFilledRect(screen, float32(barX), float32(barY), float32(fillWidth), float32(barHeight), progressColor, false)
	}

	// Draw progress indicator (current position)
	indicatorX := float64(barX) + progress*float64(barWidth)
	indicatorColor := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	vector.DrawFilledCircle(screen, float32(indicatorX), float32(barY+barHeight/2), 8, indicatorColor, false)
	vector.StrokeCircle(screen, float32(indicatorX), float32(barY+barHeight/2), 8, 2, color.RGBA{R: 100, G: 110, B: 130, A: 255}, false)

	// Draw time labels
	currentTime := formatDuration(g.audioPosition)
	totalTime := formatDuration(g.audioDuration)

	// Current time (left)
	ebitenutil.DebugPrintAt(screen, currentTime, int(barX), int(barY+barHeight+5))

	// Total time (right)
	totalTimeWidth := len(totalTime) * 8
	ebitenutil.DebugPrintAt(screen, totalTime, int(barX+barWidth-totalTimeWidth), int(barY+barHeight+5))

	// Draw hover effect
	if g.progressBarHovered {
		// Show time tooltip at mouse position
		mouseX, mouseY := ebiten.CursorPosition()
		if mouseX >= barX && mouseX <= barX+barWidth && mouseY >= barY && mouseY <= barY+barHeight {
			// Calculate time at mouse position
			mouseProgress := float64(mouseX-barX) / float64(barWidth)
			mouseTime := time.Duration(mouseProgress * float64(g.audioDuration))
			tooltipTime := formatDuration(mouseTime)

			// Draw tooltip background
			tooltipWidth := len(tooltipTime)*8 + 10
			tooltipX := mouseX - tooltipWidth/2
			tooltipY := mouseY - 25

			if tooltipX < 0 {
				tooltipX = 0
			}
			if tooltipX+tooltipWidth > windowWidth {
				tooltipX = windowWidth - tooltipWidth
			}

			vector.DrawFilledRect(screen, float32(tooltipX), float32(tooltipY), float32(tooltipWidth), 20, color.RGBA{R: 0, G: 0, B: 0, A: 200}, false)
			vector.StrokeRect(screen, float32(tooltipX), float32(tooltipY), float32(tooltipWidth), 20, 1, color.RGBA{R: 100, G: 110, B: 130, A: 255}, false)

			// Draw tooltip text
			ebitenutil.DebugPrintAt(screen, tooltipTime, tooltipX+5, tooltipY+5)
		}
	}
}

// formatDuration formats a duration as MM:SS
func formatDuration(d time.Duration) string {
	minutes := int(d.Minutes())
	seconds := int(d.Seconds()) % 60
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}

func main() {
	ebiten.SetWindowSize(windowWidth, windowHeight)
	ebiten.SetWindowTitle("AI Audio Visualizer - Click button to open file, Space: Play/Pause, Esc/Q: Quit")

	g := NewGame()
	if err := ebiten.RunGame(g); err != nil && !errors.Is(err, ebiten.Termination) {
		panic(err)
	}
}
