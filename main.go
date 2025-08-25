package main

import (
	"errors"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/iburimskiy/audio-visualization/internal/game"
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

func main() {
	ebiten.SetWindowSize(windowWidth, windowHeight)
	ebiten.SetWindowTitle("AI Audio Visualizer - Click button to open file, Space: Play/Pause, Esc/Q: Quit")

	g := game.NewGame()
	if err := ebiten.RunGame(g); err != nil && !errors.Is(err, ebiten.Termination) {
		panic(err)
	}
}
