package game

import (
	"sync"

	"github.com/faiface/beep"
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
