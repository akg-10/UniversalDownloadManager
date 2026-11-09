package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dm-engine/pkg/downloader"

	"github.com/google/uuid"
)

// Manager coordinates the download queue, persistence, and concurrent execution.
type Manager struct {
	mu           sync.RWMutex
	items        []*DownloadItem
	settings     Settings
	settingsPath string
	queuePath    string
	bridgePort   int
	broadcaster  func(event string, data any)
	ctx          context.Context
	cancel       context.CancelFunc
}

func DefaultSettings() Settings {
	home, _ := os.UserHomeDir()
	downloads := filepath.Join(home, "Downloads")
	temp := filepath.Join(os.TempDir(), "AntigravityDM")
	_ = os.MkdirAll(downloads, 0755)
	_ = os.MkdirAll(temp, 0755)

	return Settings{
		DownloadDir:      downloads,
		TempDir:          temp,
		MaxConcurrent:    3,
		DefaultParts:     8,
		ClipboardWatcher: true,
	}
}

// NewManager creates and initializes a queue manager.
func NewManager(dataDir string, bridgePort int, broadcaster func(event string, data any)) *Manager {
	_ = os.MkdirAll(dataDir, 0755)
	settingsFile := filepath.Join(dataDir, "settings.json")
	queueFile := filepath.Join(dataDir, "queue.json")

	ctx, cancel := context.WithCancel(context.Background())

	m := &Manager{
		items:        make([]*DownloadItem, 0),
		settings:     DefaultSettings(),
		settingsPath: settingsFile,
		queuePath:    queueFile,
		bridgePort:   bridgePort,
		broadcaster:  broadcaster,
		ctx:          ctx,
		cancel:       cancel,
	}

	m.loadSettings()
	m.loadQueue()

	// Launch background queue scheduler
	go m.schedulerLoop()

	return m
}

func (m *Manager) Stop() {
	m.cancel()
	m.mu.Lock()
	for _, it := range m.items {
		if it.Status == StatusDownloading && it.cancel != nil {
			it.cancel()
			it.Status = StatusPaused
		}
	}
	m.mu.Unlock()
	m.saveQueue()
}

func (m *Manager) GetSettings() Settings {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.settings
}

func (m *Manager) UpdateSettings(s Settings) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if s.DownloadDir != "" {
		_ = os.MkdirAll(s.DownloadDir, 0755)
		m.settings.DownloadDir = s.DownloadDir
	}
	if s.TempDir != "" {
		_ = os.MkdirAll(s.TempDir, 0755)
		m.settings.TempDir = s.TempDir
	}
	if s.MaxConcurrent > 0 {
		m.settings.MaxConcurrent = s.MaxConcurrent
	}
	if s.DefaultParts > 0 {
		m.settings.DefaultParts = s.DefaultParts
	}
	m.settings.ClipboardWatcher = s.ClipboardWatcher

	return m.saveSettings()
}

func (m *Manager) GetItems() []*DownloadItem {
	m.mu.RLock()
	defer m.mu.RUnlock()
	copied := make([]*DownloadItem, len(m.items))
	copy(copied, m.items)
	return copied
}

func (m *Manager) AddItem(targetURL, customName, customDir string, parts int) (*DownloadItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	targetURL = strings.TrimSpace(targetURL)
	if targetURL == "" {
		return nil, errors.New("URL cannot be empty")
	}

	if parts <= 0 {
		parts = m.settings.DefaultParts
	}

	isTg := strings.Contains(targetURL, "t.me/")

	item := &DownloadItem{
		ID:           uuid.New().String(),
		URL:          targetURL,
		Filename:     customName,
		CustomDir:    strings.TrimSpace(customDir),
		Status:       StatusQueued,
		CreatedAt:    time.Now(),
		Parts:        parts,
		IsTelegram:   isTg,
		ErrorMessage: "",
	}

	m.items = append(m.items, item)
	m.saveQueue()
	m.notifyUpdate()

	return item, nil
}

func (m *Manager) PauseItem(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, it := range m.items {
		if it.ID == id {
			if it.Status == StatusDownloading && it.cancel != nil {
				it.cancel()
			}
			it.Status = StatusPaused
			m.saveQueue()
			m.notifyUpdate()
			return nil
		}
	}
	return errors.New("item not found")
}

func (m *Manager) ResumeItem(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, it := range m.items {
		if it.ID == id {
			if it.Status == StatusPaused || it.Status == StatusError {
				it.Status = StatusQueued
				it.ErrorMessage = ""
				m.saveQueue()
				m.notifyUpdate()
				return nil
			}
		}
	}
	return errors.New("item not found")
}

func (m *Manager) DeleteItem(id string, deleteDiskFile bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	idx := -1
	var foundItem *DownloadItem
	for i, it := range m.items {
		if it.ID == id {
			idx = i
			foundItem = it
			break
		}
	}

	if idx == -1 {
		return errors.New("item not found")
	}

	if foundItem.Status == StatusDownloading && foundItem.cancel != nil {
		foundItem.cancel()
	}

	m.items = append(m.items[:idx], m.items[idx+1:]...)
	m.saveQueue()
	m.notifyUpdate()

	if deleteDiskFile && foundItem.Destination != "" {
		_ = os.Remove(foundItem.Destination)
		_ = downloader.DeleteState(foundItem.Destination)
	}

	return nil
}

func (m *Manager) PauseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, it := range m.items {
		if it.Status == StatusDownloading && it.cancel != nil {
			it.cancel()
		}
		if it.Status == StatusQueued || it.Status == StatusDownloading {
			it.Status = StatusPaused
		}
	}
	m.saveQueue()
	m.notifyUpdate()
}

func (m *Manager) ResumeAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, it := range m.items {
		if it.Status == StatusPaused || it.Status == StatusError {
			it.Status = StatusQueued
			it.ErrorMessage = ""
		}
	}
	m.saveQueue()
	m.notifyUpdate()
}

func (m *Manager) schedulerLoop() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.processQueue()
		}
	}
}

func (m *Manager) processQueue() {
	m.mu.Lock()
	activeCount := 0
	for _, it := range m.items {
		if it.Status == StatusDownloading {
			activeCount++
		}
	}

	slots := m.settings.MaxConcurrent - activeCount
	if slots <= 0 {
		m.mu.Unlock()
		return
	}

	var toStart []*DownloadItem
	for _, it := range m.items {
		if it.Status == StatusQueued {
			toStart = append(toStart, it)
			slots--
			if slots <= 0 {
				break
			}
		}
	}
	m.mu.Unlock()

	for _, item := range toStart {
		go m.startDownloadWorker(item)
	}
}

func (m *Manager) startDownloadWorker(item *DownloadItem) {
	ctx, cancel := context.WithCancel(m.ctx)

	m.mu.Lock()
	item.Status = StatusDownloading
	item.cancel = cancel
	tempDir := m.settings.TempDir
	downloadDir := m.settings.DownloadDir
	if item.CustomDir != "" {
		downloadDir = item.CustomDir
	}
	_ = os.MkdirAll(downloadDir, 0755)
	parts := item.Parts
	m.mu.Unlock()

	m.notifyUpdate()

	downloadURL := item.URL
	if item.IsTelegram {
		downloadURL = fmt.Sprintf("http://127.0.0.1:%d/stream?link=%s", m.bridgePort, item.URL)
	}

	cfg := downloader.DefaultConfig()
	cfg.Concurrency = parts
	cfg.OutputDir = tempDir
	if item.Filename != "" {
		cfg.Filename = item.Filename
	}

	dl := downloader.New(cfg)

	progressCb := func(p downloader.ProgressInfo) {
		m.mu.Lock()
		item.TotalSize = p.TotalSize
		item.DownloadedBytes = p.DownloadedBytes
		item.Percentage = p.Percentage
		item.SpeedBps = p.SpeedBps
		if p.ETA > 0 {
			item.ETA = p.ETA.Round(time.Second).String()
		} else {
			item.ETA = "--:--"
		}
		m.mu.Unlock()
	}

	state, err := dl.Download(ctx, downloadURL, progressCb)

	m.mu.Lock()
	defer m.mu.Unlock()

	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || item.Status == StatusPaused {
			item.Status = StatusPaused
		} else {
			item.Status = StatusError
			item.ErrorMessage = err.Error()
		}
	} else if state != nil {
		item.Status = StatusCompleted
		item.Percentage = 100.0
		item.TotalSize = state.TotalSize
		item.DownloadedBytes = state.TotalSize
		item.SpeedBps = 0
		item.ETA = "Complete"

		// Move from TempDir to final DownloadDir if directories differ
		finalName := filepath.Base(state.Destination)
		if item.Filename != "" {
			finalName = item.Filename
		}
		item.Filename = finalName
		finalPath := filepath.Join(downloadDir, finalName)

		if filepath.Clean(state.Destination) != filepath.Clean(finalPath) {
			_ = moveFile(state.Destination, finalPath)
			item.Destination = finalPath
		} else {
			item.Destination = state.Destination
		}
	}

	m.saveQueue()
	m.notifyUpdate()
}

func (m *Manager) notifyUpdate() {
	if m.broadcaster != nil {
		m.broadcaster("queue_updated", m.items)
	}
}

func (m *Manager) loadSettings() {
	data, err := os.ReadFile(m.settingsPath)
	if err == nil {
		_ = json.Unmarshal(data, &m.settings)
	}
}

func (m *Manager) saveSettings() error {
	data, err := json.MarshalIndent(m.settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.settingsPath, data, 0644)
}

func (m *Manager) loadQueue() {
	data, err := os.ReadFile(m.queuePath)
	if err == nil {
		var loaded []*DownloadItem
		if err := json.Unmarshal(data, &loaded); err == nil {
			for _, it := range loaded {
				if it.Status == StatusDownloading {
					it.Status = StatusPaused // Resume on startup via scheduler
				}
			}
			m.items = loaded
		}
	}
}

func (m *Manager) saveQueue() {
	data, err := json.MarshalIndent(m.items, "", "  ")
	if err == nil {
		_ = os.WriteFile(m.queuePath, data, 0644)
	}
}

func moveFile(src, dst string) error {
	// Attempt fast atomic rename first
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// Fallback to cross-device copy & remove
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	_ = in.Close()
	return os.Remove(src)
}
