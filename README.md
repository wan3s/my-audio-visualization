# AI Audio Visualizer (Go)

A sophisticated Go desktop app that plays audio files with dynamic, complex visualizations including animated shapes, waves, particles, and energy rings.

### Features
- Click button to open a local audio file (.mp3, .wav, .flac)
- Play/Pause (Space)
- **Interactive Progress Bar:**
  - Shows current playback position and total duration
  - Click anywhere on the bar to seek to that position
  - Drag to scrub through the song
  - Hover to see time tooltips
  - Visual progress indicator with gradient colors
- **Complex Visualizations:**
  - Animated circles that pulse with audio
  - Dynamic wave patterns that respond to music
  - Particle effects that dance to the beat
  - Energy rings that expand and contract
  - Real-time color transitions using HSV color space
  - Dynamic background gradients
  - Smooth animations and transitions
- **Audio Analysis Bar:**
  - 64-band frequency visualization
  - Color-coded frequency segments
  - Real-time audio level display

### Controls
- **Click "Open File" button**: Open audio file dialog
- **Space**: Play/Pause
- **Click/Drag Progress Bar**: Seek through the song
- **Esc or Q**: Quit

### Requirements
- Go 1.21+
- macOS (tested), should work on Windows/Linux too
- On macOS, you may need Xcode Command Line Tools: `xcode-select --install`

### Run
```bash
cd /Users/iburimskiy/Projects/go/ai-game
go run .
```

### Notes
- Decoding and playback via `github.com/faiface/beep` + `speaker`
- Advanced visualization with `github.com/hajimehoshi/ebiten/v2` + vector graphics
- File dialog via `github.com/ncruces/zenity`
- Modern UI with clickable buttons and complex audio-reactive graphics
- Real-time audio analysis with 64 frequency bands
- Smooth color transitions and dynamic effects
- Interactive progress bar with seeking functionality

