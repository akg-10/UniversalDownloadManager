package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

// downloadChunk handles downloading a single byte range with automatic retry logic.
func downloadChunk(
	ctx context.Context,
	client *http.Client,
	targetURL string,
	userAgent string,
	chunk *ChunkState,
	file *os.File,
	onBytesRead func(n int),
	maxRetries int,
) error {
	var currentDownloaded = atomic.LoadInt64(&chunk.Downloaded)
	chunkTotal := chunk.End - chunk.Start + 1

	if currentDownloaded >= chunkTotal && chunkTotal > 0 {
		chunk.Completed = true
		return nil
	}

	buffer := make([]byte, 64*1024) // 64 KB read buffer
	var retryCount = 0
	var lastProgress = currentDownloaded

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		currentDownloaded = atomic.LoadInt64(&chunk.Downloaded)
		if currentDownloaded > lastProgress {
			retryCount = 0
			lastProgress = currentDownloaded
		}
		startOffset := chunk.Start + currentDownloaded
		endOffset := chunk.End

		if startOffset > endOffset && endOffset > 0 {
			chunk.Completed = true
			return nil
		}

		req, err := http.NewRequestWithContext(ctx, "GET", targetURL, nil)
		if err != nil {
			return err
		}

		if userAgent != "" {
			req.Header.Set("User-Agent", userAgent)
		} else {
			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Antigravity-DM/1.0")
		}

		if endOffset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", startOffset, endOffset))
		}

		resp, err := client.Do(req)
		if err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				return ctx.Err()
			}

			retryCount++
			if retryCount > maxRetries {
				return fmt.Errorf("chunk %d reached max retries (%d): %w", chunk.Index, maxRetries, err)
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(retryCount*500) * time.Millisecond):
				continue
			}
		}

		// Verify 206 Partial Content (or 200 if whole single chunk)
		if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			retryCount++
			if retryCount > maxRetries {
				return fmt.Errorf("chunk %d HTTP status %d: %s", chunk.Index, resp.StatusCode, resp.Status)
			}
			time.Sleep(1 * time.Second)
			continue
		}

		// Read and write directly to file at calculated byte offset
		writeOffset := startOffset
		streamErr := func() error {
			defer resp.Body.Close()

			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}

				n, rErr := resp.Body.Read(buffer)
				if n > 0 {
					// Direct random-access write at specific offset.
					// os.File.WriteAt is concurrency-safe on Windows without mutexes.
					_, wErr := file.WriteAt(buffer[:n], writeOffset)
					if wErr != nil {
						return fmt.Errorf("disk write failure at offset %d: %w", writeOffset, wErr)
					}

					writeOffset += int64(n)
					atomic.AddInt64(&chunk.Downloaded, int64(n))

					if onBytesRead != nil {
						onBytesRead(n)
					}
				}

				if rErr != nil {
					if errors.Is(rErr, io.EOF) {
						return nil // Finished this stream cleanly
					}
					return rErr
				}
			}
		}()

		if streamErr == nil {
			// Stream completed
			currentDownloaded = atomic.LoadInt64(&chunk.Downloaded)
			if currentDownloaded >= chunkTotal {
				chunk.Completed = true
				return nil
			}
		}

		if errors.Is(ctx.Err(), context.Canceled) {
			return ctx.Err()
		}

		// If disconnected early without EOF, retry remaining bytes
		retryCount++
		if retryCount > maxRetries {
			return fmt.Errorf("chunk %d dropped and exceeded max retries: %w", chunk.Index, streamErr)
		}
		time.Sleep(1 * time.Second)
	}
}
