package queue

import (
	"context"
	"time"
)

type DownloadStatus string

const (
	StatusQueued      DownloadStatus = "Queued"
	StatusDownloading DownloadStatus = "Downloading"
	StatusPaused      DownloadStatus = "Paused"
	StatusCompleted   DownloadStatus = "Completed"
	StatusError       DownloadStatus = "Error"
)

// DownloadItem represents a single task in the queue.
type DownloadItem struct {
	ID              string         `json:"id"`
	URL             string         `json:"url"`
	Filename        string         `json:"filename"`
	CustomDir       string         `json:"custom_dir,omitempty"`
	Destination     string         `json:"destination"`
	TotalSize       int64          `json:"total_size"`
	DownloadedBytes int64          `json:"downloaded_bytes"`
	Percentage      float64        `json:"percentage"`
	SpeedBps        float64        `json:"speed_bps"`
	ETA             string         `json:"eta"`
	Status          DownloadStatus `json:"status"`
	ErrorMessage    string         `json:"error_message,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	Parts           int            `json:"parts"`
	IsTelegram      bool           `json:"is_telegram"`

	cancel context.CancelFunc `json:"-"`
}

// Settings stores user configuration.
type Settings struct {
	DownloadDir      string `json:"download_dir"`
	TempDir          string `json:"temp_dir"`
	MaxConcurrent    int    `json:"max_concurrent"`
	DefaultParts     int    `json:"default_parts"`
	ClipboardWatcher bool   `json:"clipboard_watcher"`
}
