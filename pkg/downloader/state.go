package downloader

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// ChunkState represents the download state of an individual byte slice.
type ChunkState struct {
	Index      int   `json:"index"`
	Start      int64 `json:"start"`
	End        int64 `json:"end"`
	Downloaded int64 `json:"downloaded"`
	Completed  bool  `json:"completed"`
}

// DownloadState stores complete metadata and per-chunk progress to enable resume.
type DownloadState struct {
	URL          string        `json:"url"`
	Destination  string        `json:"destination"`
	TotalSize    int64         `json:"total_size"`
	Resumable    bool          `json:"resumable"`
	ETag         string        `json:"etag"`
	LastModified string        `json:"last_modified"`
	Chunks       []*ChunkState `json:"chunks"`

	mu sync.Mutex `json:"-"`
}

// StateFilePath returns the path to the sidecar checkpoint file for a target download.
func StateFilePath(destPath string) string {
	return destPath + ".dmstate"
}

// Save atomically writes the download state to disk.
func (s *DownloadState) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	statePath := StateFilePath(s.Destination)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize state: %w", err)
	}

	// Atomic write: write to temp file then rename
	tmpPath := statePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp state file: %w", err)
	}

	if err := os.Rename(tmpPath, statePath); err != nil {
		// On Windows, Rename fails if destination exists in some cases, so remove first
		_ = os.Remove(statePath)
		if err := os.Rename(tmpPath, statePath); err != nil {
			return fmt.Errorf("failed to commit state file: %w", err)
		}
	}

	return nil
}

// LoadState reads an existing state checkpoint from disk.
func LoadState(destPath string) (*DownloadState, error) {
	statePath := StateFilePath(destPath)
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil, err
	}

	var state DownloadState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("corrupted state file: %w", err)
	}

	return &state, nil
}

// DeleteState cleans up the checkpoint file upon successful download completion.
func DeleteState(destPath string) error {
	statePath := StateFilePath(destPath)
	if err := os.Remove(statePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = os.Remove(statePath + ".tmp")
	return nil
}

// HasState checks if a valid resume state exists for the target destination.
func HasState(destPath string) bool {
	statePath := StateFilePath(destPath)
	info, err := os.Stat(statePath)
	return err == nil && !info.IsDir() && info.Size() > 0
}

// PartitionChunks calculates non-overlapping chunk ranges across total bytes.
func PartitionChunks(totalSize int64, numChunks int) []*ChunkState {
	if numChunks <= 0 {
		numChunks = 1
	}
	if totalSize <= 0 {
		return []*ChunkState{
			{Index: 0, Start: 0, End: -1, Downloaded: 0, Completed: false},
		}
	}

	// If file is very small (e.g. < 512KB), keep to 1 chunk to avoid HTTP overhead
	if totalSize < 512*1024 {
		numChunks = 1
	}

	chunks := make([]*ChunkState, numChunks)
	chunkSize := totalSize / int64(numChunks)

	for i := 0; i < numChunks; i++ {
		start := int64(i) * chunkSize
		end := start + chunkSize - 1
		if i == numChunks-1 {
			end = totalSize - 1 // Last chunk absorbs remainder
		}

		chunks[i] = &ChunkState{
			Index:      i,
			Start:      start,
			End:        end,
			Downloaded: 0,
			Completed:  false,
		}
	}

	return chunks
}
