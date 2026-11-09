package clipboard

import (
	"context"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	openClipboard    = user32.NewProc("OpenClipboard")
	closeClipboard   = user32.NewProc("CloseClipboard")
	getClipboardData = user32.NewProc("GetClipboardData")
	globalLock       = kernel32.NewProc("GlobalLock")
	globalUnlock     = kernel32.NewProc("GlobalUnlock")
)

const cfUnicodeText = 13 // CF_UNICODETEXT

// ReadText retrieves text from the Windows clipboard using native Win32 APIs.
func ReadText() (string, error) {
	r1, _, err := openClipboard.Call(0)
	if r1 == 0 {
		return "", err
	}
	defer closeClipboard.Call()

	hData, _, err := getClipboardData.Call(cfUnicodeText)
	if hData == 0 {
		return "", nil // Clipboard has no text
	}

	ptr, _, err := globalLock.Call(hData)
	if ptr == 0 {
		return "", err
	}
	defer globalUnlock.Call(hData)

	text := syscall.UTF16ToString((*[1 << 20]uint16)(unsafe.Pointer(ptr))[:])
	return text, nil
}

// Watch starts monitoring the clipboard for newly copied downloadable links.
func Watch(ctx context.Context, onLinkDetected func(link string)) {
	var lastText string

	ticker := time.NewTicker(750 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current, err := ReadText()
			if err != nil || current == "" {
				continue
			}

			current = strings.TrimSpace(current)
			if current == lastText {
				continue
			}
			lastText = current

			// Sniff if this is a web link or telegram link
			if strings.HasPrefix(current, "http://") || strings.HasPrefix(current, "https://") || strings.HasPrefix(current, "t.me/") {
				if onLinkDetected != nil {
					onLinkDetected(current)
				}
			}
		}
	}
}
