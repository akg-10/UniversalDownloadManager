package downloader

import (
	"sync"
	"time"
)

// ChunkProgressInfo provides snapshot telemetry for a single worker slice.
type ChunkProgressInfo struct {
	Index      int     `json:"index"`
	Start      int64   `json:"start"`
	End        int64   `json:"end"`
	Total      int64   `json:"total"`
	Downloaded int64   `json:"downloaded"`
	Percentage float64 `json:"percentage"`
	Active     bool    `json:"active"`
	Completed  bool    `json:"completed"`
}

// ProgressInfo represents a complete telemetry snapshot of the overall download.
type ProgressInfo struct {
	TotalSize       int64               `json:"total_size"`
	DownloadedBytes int64               `json:"downloaded_bytes"`
	Percentage      float64             `json:"percentage"`
	SpeedBps        float64             `json:"speed_bps"` // Bytes per second
	ETA             time.Duration       `json:"eta"`
	Chunks          []ChunkProgressInfo `json:"chunks"`
}

type speedSample struct {
	timestamp time.Time
	bytes     int64
}

// SpeedTracker calculates smoothed transfer rates using a rolling time window.
type SpeedTracker struct {
	mu      sync.Mutex
	samples []speedSample
	window  time.Duration
}

// NewSpeedTracker creates a tracker with a rolling window (typically 2-3 seconds).
func NewSpeedTracker(window time.Duration) *SpeedTracker {
	return &SpeedTracker{
		window:  window,
		samples: make([]speedSample, 0, 32),
	}
}

// Record appends a downloaded byte count at current timestamp.
func (st *SpeedTracker) Record(currentBytes int64) {
	st.mu.Lock()
	defer st.mu.Unlock()

	now := time.Now()
	st.samples = append(st.samples, speedSample{timestamp: now, bytes: currentBytes})

	// Evict samples outside the window
	cutoff := now.Add(-st.window)
	idx := 0
	for idx < len(st.samples) && st.samples[idx].timestamp.Before(cutoff) {
		idx++
	}
	if idx > 0 {
		st.samples = st.samples[idx:]
	}
}

// CurrentSpeed returns the average speed in bytes per second within the window.
func (st *SpeedTracker) CurrentSpeed() float64 {
	st.mu.Lock()
	defer st.mu.Unlock()

	if len(st.samples) < 2 {
		return 0.0
	}

	first := st.samples[0]
	last := st.samples[len(st.samples)-1]

	duration := last.timestamp.Sub(first.timestamp).Seconds()
	if duration <= 0.05 {
		return 0.0
	}

	byteDelta := last.bytes - first.bytes
	if byteDelta <= 0 {
		return 0.0
	}

	return float64(byteDelta) / duration
}
