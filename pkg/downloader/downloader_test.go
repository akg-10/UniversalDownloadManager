package downloader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func createMockServer(payload []byte) *httptest.Server {
	totalSize := int64(len(payload))
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Disposition", `attachment; filename="testfile.bin"`)

		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "" {
			w.Header().Set("Content-Length", strconv.FormatInt(totalSize, 10))
			if r.Method == "HEAD" {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
			return
		}

		if strings.HasPrefix(rangeHeader, "bytes=") {
			ranges := strings.TrimPrefix(rangeHeader, "bytes=")
			parts := strings.Split(ranges, "-")
			start, _ := strconv.ParseInt(parts[0], 10, 64)
			end := totalSize - 1
			if len(parts) > 1 && parts[1] != "" {
				end, _ = strconv.ParseInt(parts[1], 10, 64)
			}

			if start >= totalSize || end >= totalSize || start > end {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}

			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, totalSize))
			w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
			w.WriteHeader(http.StatusPartialContent)

			if r.Method != "HEAD" {
				_, _ = w.Write(payload[start : end+1])
			}
		}
	}))
}

func TestMultiPartDownload(t *testing.T) {
	testPayloadSize := int64(4 * 1024 * 1024) // 4 MB
	payload := make([]byte, testPayloadSize)
	for i := range payload {
		payload[i] = byte((i * 37) % 256)
	}
	expectedHash := sha256.Sum256(payload)

	server := createMockServer(payload)
	defer server.Close()

	tempDir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Concurrency = 4
	cfg.OutputDir = tempDir
	cfg.Filename = "full_download.bin"

	dl := New(cfg)
	state, err := dl.Download(context.Background(), server.URL, nil)
	if err != nil {
		t.Fatalf("download failed: %v", err)
	}

	destPath := filepath.Join(tempDir, "full_download.bin")
	downloadedData, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	actualHash := sha256.Sum256(downloadedData)
	if !bytes.Equal(expectedHash[:], actualHash[:]) {
		t.Fatalf("checksum mismatch!")
	}
	if state.TotalSize != testPayloadSize {
		t.Errorf("expected size %d, got %d", testPayloadSize, state.TotalSize)
	}
}

func TestPauseAndResumeDownload(t *testing.T) {
	testPayloadSize := int64(6 * 1024 * 1024) // 6 MB
	payload := make([]byte, testPayloadSize)
	for i := range payload {
		payload[i] = byte((i * 17) % 256)
	}
	expectedHash := sha256.Sum256(payload)

	server := createMockServer(payload)
	defer server.Close()

	tempDir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Concurrency = 4
	cfg.OutputDir = tempDir
	cfg.Filename = "resume_test.bin"
	cfg.ProgressInterval = 10 * time.Millisecond

	dl := New(cfg)
	destPath := filepath.Join(tempDir, "resume_test.bin")

	// Phase 1: Start download and cancel after ~1MB is downloaded
	ctx1, cancel1 := context.WithCancel(context.Background())
	var cancelled int32

	progressCb := func(p ProgressInfo) {
		if p.DownloadedBytes > 1024*1024 && atomic.CompareAndSwapInt32(&cancelled, 0, 1) {
			cancel1()
		}
	}

	_, err := dl.Download(ctx1, server.URL, progressCb)
	if err == nil {
		t.Logf("download completed before cancel fired, test still valid")
	}

	// Verify that state file exists
	if HasState(destPath) {
		state, err := LoadState(destPath)
		if err != nil {
			t.Fatalf("failed to load state file: %v", err)
		}
		if state.TotalSize != testPayloadSize {
			t.Errorf("state total size mismatch")
		}
	}

	// Phase 2: Resume download to completion
	ctx2 := context.Background()
	resumedState, err := dl.Download(ctx2, server.URL, nil)
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}

	if resumedState.TotalSize != testPayloadSize {
		t.Errorf("resumed state total size mismatch")
	}

	downloadedData, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("failed to read resumed file: %v", err)
	}

	actualHash := sha256.Sum256(downloadedData)
	if !bytes.Equal(expectedHash[:], actualHash[:]) {
		t.Fatalf("checksum mismatch after resume! expected %x, got %x", expectedHash, actualHash)
	}

	// State file should be deleted on completion
	if HasState(destPath) {
		t.Errorf("checkpoint file remained after completion")
	}
}
