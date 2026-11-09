package tgbridge

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// StreamTelegramFile serves an HTTP request for a Telegram document with full byte-range support.
func StreamTelegramFile(
	ctx context.Context,
	defaultRaw *tg.Client,
	getDCClient func(ctx context.Context, dcID int) (*tg.Client, error),
	meta *FileMetadata,
	w http.ResponseWriter,
	r *http.Request,
) {
	totalSize := meta.Size
	w.Header().Set("Accept-Ranges", "bytes")
	if meta.MimeType != "" {
		w.Header().Set("Content-Type", meta.MimeType)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, sanitizeDisposition(meta.Filename)))

	// Pre-resolve target DC once upfront to prevent worker race conditions
	activeClient := defaultRaw
	probeRes, probeErr := defaultRaw.UploadGetFile(ctx, &tg.UploadGetFileRequest{
		Precise:  true,
		Location: meta.Location,
		Offset:   0,
		Limit:    4096,
	})
	if probeErr != nil {
		if rpcErr, ok := tgerr.As(probeErr); ok && rpcErr.Code == 303 {
			targetDC := rpcErr.Argument
			if targetDC > 0 && getDCClient != nil {
				dcClient, dcErr := getDCClient(ctx, targetDC)
				if dcErr == nil {
					activeClient = dcClient
				}
			}
		}
	}
	_ = probeRes

	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" {
		w.Header().Set("Content-Length", strconv.FormatInt(totalSize, 10))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		streamByteSlice(ctx, activeClient, getDCClient, meta.Location, 0, totalSize-1, w)
		return
	}

	if !strings.HasPrefix(rangeHeader, "bytes=") {
		http.Error(w, "invalid range header", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	rawRange := strings.TrimPrefix(rangeHeader, "bytes=")
	parts := strings.Split(rawRange, "-")
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 || start >= totalSize {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", totalSize))
		http.Error(w, "invalid range start", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	end := totalSize - 1
	if len(parts) > 1 && parts[1] != "" {
		if parsedEnd, err := strconv.ParseInt(parts[1], 10, 64); err == nil && parsedEnd >= start {
			if parsedEnd < totalSize {
				end = parsedEnd
			}
		}
	}

	contentLength := end - start + 1
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, totalSize))
	w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
	w.WriteHeader(http.StatusPartialContent)

	if r.Method == http.MethodHead {
		return
	}

	streamByteSlice(ctx, activeClient, getDCClient, meta.Location, start, end, w)
}

type chunkTask struct {
	index  int
	offset int64
}

type chunkResult struct {
	index int
	data  []byte
	err   error
}

func streamByteSlice(
	ctx context.Context,
	initialRaw *tg.Client,
	getDCClient func(ctx context.Context, dcID int) (*tg.Client, error),
	loc *tg.InputDocumentFileLocation,
	start, end int64,
	w http.ResponseWriter,
) {
	flusher, _ := w.(http.Flusher)

	// 512 KB chunk size
	const chunkSize = int64(512 * 1024)
	alignedOffset := (start / chunkSize) * chunkSize
	currentPosition := start
	bytesRemaining := end - start + 1

	var tasks []chunkTask
	curr := alignedOffset
	idx := 0
	for curr <= end {
		tasks = append(tasks, chunkTask{index: idx, offset: curr})
		curr += chunkSize
		idx++
	}

	if len(tasks) == 0 {
		return
	}

	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()

	taskChan := make(chan chunkTask, len(tasks))
	for _, t := range tasks {
		taskChan <- t
	}
	close(taskChan)

	numWorkers := 4
	if len(tasks) < numWorkers {
		numWorkers = len(tasks)
	}

	resultChan := make(chan chunkResult, numWorkers*2)

	var activeClient atomic.Pointer[tg.Client]
	activeClient.Store(initialRaw)

	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for t := range taskChan {
				select {
				case <-streamCtx.Done():
					return
				default:
				}

				var data []byte
				var fetchErr error

				for attempt := 0; attempt < 10; attempt++ {
					client := activeClient.Load()
					res, err := client.UploadGetFile(streamCtx, &tg.UploadGetFileRequest{
						Precise:  true,
						Location: loc,
						Offset:   t.offset,
						Limit:    int(chunkSize),
					})

					if err == nil {
						if f, ok := res.(*tg.UploadFile); ok {
							data = f.Bytes
							fetchErr = nil
							break
						}
					}

					fetchErr = err

					// Check for DC migration (code 303: FILE_MIGRATE)
					if rpcErr, ok := tgerr.As(err); ok && rpcErr.Code == 303 {
						targetDC := rpcErr.Argument
						if targetDC > 0 && getDCClient != nil {
							newClient, dcErr := getDCClient(streamCtx, targetDC)
							if dcErr == nil {
								activeClient.Store(newClient)
								continue
							}
						}
					}

					// Handle FloodWait
					if wait, isFlood := tgerr.AsFloodWait(err); isFlood {
						select {
						case <-streamCtx.Done():
							return
						case <-time.After(wait + 200*time.Millisecond):
							continue
						}
					}

					select {
					case <-streamCtx.Done():
						return
					case <-time.After(time.Duration(attempt+1) * 350 * time.Millisecond):
					}
				}

				select {
				case resultChan <- chunkResult{index: t.index, data: data, err: fetchErr}:
				case <-streamCtx.Done():
					return
				}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	readyChunks := make(map[int][]byte)
	expectedIndex := 0

	for res := range resultChan {
		if res.err != nil {
			return
		}

		readyChunks[res.index] = res.data

		for {
			chunkBytes, exists := readyChunks[expectedIndex]
			if !exists {
				break
			}
			delete(readyChunks, expectedIndex)
			expectedIndex++

			payloadOffset := alignedOffset + int64(expectedIndex-1)*chunkSize
			skipOffset := int64(0)
			if currentPosition > payloadOffset {
				skipOffset = currentPosition - payloadOffset
			}
			if skipOffset >= int64(len(chunkBytes)) {
				continue
			}

			data := chunkBytes[skipOffset:]
			if int64(len(data)) > bytesRemaining {
				data = data[:bytesRemaining]
			}

			if len(data) > 0 {
				n, err := w.Write(data)
				if err != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
				currentPosition += int64(n)
				bytesRemaining -= int64(n)
			}

			if bytesRemaining <= 0 {
				return
			}
		}
	}
}

func sanitizeDisposition(name string) string {
	return strings.ReplaceAll(name, `"`, "")
}
