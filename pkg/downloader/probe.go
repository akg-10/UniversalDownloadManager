package downloader

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// FileInfo holds metadata about a remote resource.
type FileInfo struct {
	URL          string
	Filename     string
	TotalSize    int64
	AcceptRanges bool
	ETag         string
	LastModified string
}

var invalidFilenameChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

// ProbeURL inspects the given URL to retrieve file size, filename, and range capability.
func ProbeURL(targetURL string, userAgent string) (*FileInfo, error) {
	parsedURL, err := url.ParseRequestURI(targetURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			if userAgent != "" {
				req.Header.Set("User-Agent", userAgent)
			}
			return nil
		},
	}

	// 1. Try HEAD request first
	req, err := http.NewRequest("HEAD", targetURL, nil)
	if err != nil {
		return nil, err
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	} else {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Universal-Download-Manager/1.0")
	}

	resp, err := client.Do(req)
	var fallbackToRangeGet bool

	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 || resp.ContentLength <= 0 {
		// Some servers/CDNs reject HEAD or return Content-Length: 0 on HEAD.
		// Fall back to a GET request probing range 0-0.
		fallbackToRangeGet = true
	}

	if fallbackToRangeGet {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}

		getReq, err := http.NewRequest("GET", targetURL, nil)
		if err != nil {
			return nil, err
		}
		if userAgent != "" {
			getReq.Header.Set("User-Agent", userAgent)
		} else {
			getReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Universal-Download-Manager/1.0")
		}
		getReq.Header.Set("Range", "bytes=0-0")

		resp, err = client.Do(getReq)
		if err != nil {
			return nil, fmt.Errorf("probe request failed: %w", err)
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("server returned unexpected HTTP status: %s", resp.Status)
	}

	info := &FileInfo{
		URL:          resp.Request.URL.String(),
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("LastModified"),
	}

	// Range capability detection
	if resp.StatusCode == http.StatusPartialContent {
		info.AcceptRanges = true
	} else if strings.EqualFold(resp.Header.Get("Accept-Ranges"), "bytes") {
		info.AcceptRanges = true
	}

	// Content-Length / Total size determination
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		// Content-Range: bytes 0-0/12345678
		parts := strings.Split(cr, "/")
		if len(parts) == 2 && parts[1] != "*" {
			if sz, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
				info.TotalSize = sz
				info.AcceptRanges = true
			}
		}
	}

	if info.TotalSize <= 0 && resp.ContentLength > 0 {
		info.TotalSize = resp.ContentLength
	}

	// Filename extraction
	info.Filename = extractFilename(resp, parsedURL)

	return info, nil
}

func extractFilename(resp *http.Response, parsedURL *url.URL) string {
	cd := resp.Header.Get("Content-Disposition")
	if cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if filename, ok := params["filename*"]; ok {
				// Format: UTF-8''encoded_name
				if idx := strings.Index(filename, "''"); idx != -1 {
					decoded, err := url.QueryUnescape(filename[idx+2:])
					if err == nil && decoded != "" {
						return sanitizeFilename(decoded)
					}
				}
			}
			if filename, ok := params["filename"]; ok && filename != "" {
				return sanitizeFilename(filename)
			}
		}
	}

	// Fallback to URL path
	base := path.Base(parsedURL.Path)
	if base != "" && base != "." && base != "/" {
		if unescaped, err := url.PathUnescape(base); err == nil && unescaped != "" {
			return sanitizeFilename(unescaped)
		}
		return sanitizeFilename(base)
	}

	// Final fallback
	return fmt.Sprintf("download_%d.bin", time.Now().Unix())
}

func sanitizeFilename(name string) string {
	cleaned := invalidFilenameChars.ReplaceAllString(name, "_")
	cleaned = strings.TrimSpace(cleaned)
	cleaned = strings.Trim(cleaned, ". ")
	if cleaned == "" {
		return "download.bin"
	}
	return filepath.Clean(cleaned)
}
