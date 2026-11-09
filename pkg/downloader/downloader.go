package downloader

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Config options for initializing a Downloader instance.
type Config struct {
	Concurrency      int
	OutputDir        string
	Filename         string
	UserAgent        string
	MaxRetries       int
	ProgressInterval time.Duration
}

// DefaultConfig returns optimal defaults for high-speed segmented downloads.
func DefaultConfig() Config {
	return Config{
		Concurrency:      8,
		OutputDir:        ".",
		UserAgent:        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Antigravity-DM/1.0",
		MaxRetries:       15,
		ProgressInterval: 200 * time.Millisecond,
	}
}

// Downloader orchestrates probing, partitioning, parallel streaming, and state saving.
type Downloader struct {
	config Config
	client *http.Client
}

// New creates a new Downloader configured for high-performance HTTP I/O.
func New(cfg Config) *Downloader {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 8
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 5
	}
	if cfg.ProgressInterval <= 0 {
		cfg.ProgressInterval = 200 * time.Millisecond
	}

	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: cfg.Concurrency * 2,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true, // Compression interferes with accurate byte ranges
	}

	return &Downloader{
		config: cfg,
		client: &http.Client{
			Transport: transport,
			Timeout:   0, // Individual chunk contexts govern timeouts
		},
	}
}

// Download starts or resumes a download from a target URL.
func (d *Downloader) Download(ctx context.Context, targetURL string, progressCb func(ProgressInfo)) (*DownloadState, error) {
	// 1. Probe the target URL
	info, err := ProbeURL(targetURL, d.config.UserAgent)
	if err != nil {
		return nil, fmt.Errorf("probe failed: %w", err)
	}

	// 2. Resolve destination path
	destFilename := d.config.Filename
	if destFilename == "" {
		destFilename = info.Filename
	}
	if d.config.OutputDir != "" {
		_ = os.MkdirAll(d.config.OutputDir, 0755)
	}
	destPath := filepath.Join(d.config.OutputDir, destFilename)

	// 3. Check for existing state file to resume
	var state *DownloadState
	if HasState(destPath) {
		loadedState, err := LoadState(destPath)
		if err == nil && loadedState.URL == info.URL && loadedState.TotalSize == info.TotalSize {
			state = loadedState
		}
	}

	// 4. Initialize fresh state if not resuming
	if state == nil {
		numChunks := d.config.Concurrency
		if !info.AcceptRanges || info.TotalSize <= 0 {
			numChunks = 1 // Single stream required if range requests unsupported
		}

		state = &DownloadState{
			URL:          info.URL,
			Destination:  destPath,
			TotalSize:    info.TotalSize,
			Resumable:    info.AcceptRanges,
			ETag:         info.ETag,
			LastModified: info.LastModified,
			Chunks:       PartitionChunks(info.TotalSize, numChunks),
		}
	}

	// 5. Open and pre-allocate target file
	file, err := os.OpenFile(destPath, os.O_CREATE|os.O_RDWR, 0666)
	if err != nil {
		return nil, fmt.Errorf("failed to open destination file: %w", err)
	}
	defer file.Close()

	if state.TotalSize > 0 {
		// Pre-allocate full size on disk immediately to prevent file fragmentation
		if stat, err := file.Stat(); err == nil && stat.Size() < state.TotalSize {
			_ = file.Truncate(state.TotalSize)
		}
	}

	// 6. Setup progress & telemetry
	tracker := NewSpeedTracker(3 * time.Second)
	var totalDownloaded int64
	for _, c := range state.Chunks {
		totalDownloaded += atomic.LoadInt64(&c.Downloaded)
	}
	tracker.Record(totalDownloaded)

	// Background ticker for telemetry and periodic state checkpointing
	ticker := time.NewTicker(d.config.ProgressInterval)
	defer ticker.Stop()

	lastStateSave := time.Now()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				var currentDownloaded int64
				chunkProgress := make([]ChunkProgressInfo, len(state.Chunks))

				for i, c := range state.Chunks {
					dl := atomic.LoadInt64(&c.Downloaded)
					currentDownloaded += dl
					totalChunkBytes := c.End - c.Start + 1
					pct := 0.0
					if totalChunkBytes > 0 {
						pct = float64(dl) / float64(totalChunkBytes) * 100
					}

					chunkProgress[i] = ChunkProgressInfo{
						Index:      c.Index,
						Start:      c.Start,
						End:        c.End,
						Total:      totalChunkBytes,
						Downloaded: dl,
						Percentage: pct,
						Active:     !c.Completed && dl < totalChunkBytes,
						Completed:  c.Completed || (totalChunkBytes > 0 && dl >= totalChunkBytes),
					}
				}

				tracker.Record(currentDownloaded)
				speed := tracker.CurrentSpeed()

				overallPct := 0.0
				var eta time.Duration
				if state.TotalSize > 0 {
					overallPct = float64(currentDownloaded) / float64(state.TotalSize) * 100
					if speed > 0 {
						remBytes := state.TotalSize - currentDownloaded
						if remBytes > 0 {
							eta = time.Duration(float64(remBytes)/speed) * time.Second
						}
					}
				}

				if progressCb != nil {
					progressCb(ProgressInfo{
						TotalSize:       state.TotalSize,
						DownloadedBytes: currentDownloaded,
						Percentage:      overallPct,
						SpeedBps:        speed,
						ETA:             eta,
						Chunks:          chunkProgress,
					})
				}

				// Checkpoint state to disk every 1.5 seconds
				if time.Since(lastStateSave) > 1500*time.Millisecond {
					_ = state.Save()
					lastStateSave = time.Now()
				}
			}
		}
	}()

	// 7. Launch concurrent chunk workers
	var wg sync.WaitGroup
	errChan := make(chan error, len(state.Chunks))

	for _, chunk := range state.Chunks {
		if chunk.Completed {
			continue
		}

		wg.Add(1)
		go func(c *ChunkState) {
			defer wg.Done()

			err := downloadChunk(
				ctx,
				d.client,
				state.URL,
				d.config.UserAgent,
				c,
				file,
				nil,
				d.config.MaxRetries,
			)

			if err != nil && !errors.Is(err, context.Canceled) {
				errChan <- err
			}
		}(chunk)
	}

	wg.Wait()
	close(errChan)

	// Check if any fatal chunk error occurred
	if firstErr := <-errChan; firstErr != nil {
		_ = state.Save() // Save state before aborting so user can resume
		return nil, firstErr
	}

	if errors.Is(ctx.Err(), context.Canceled) {
		_ = state.Save() // Save state on cancellation/pause
		return state, ctx.Err()
	}

	// 8. Download succeeded: Clean up state checkpoint
	_ = DeleteState(destPath)

	return state, nil
}
