# Universal Download Manager (UDM)

[![Built with Antigravity](https://img.shields.io/badge/Built%20with-Antigravity-4285F4?style=flat-square&logo=google)](https://github.com/google-deepmind)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Platform: Windows 64-bit](https://img.shields.io/badge/Platform-Windows%2064--bit-blue.svg)](#)
[![Go Version](https://img.shields.io/badge/Go-1.22%2B-00ADD8?style=flat-square&logo=go)](https://go.dev/)

A high-performance personal Internet Download Manager (IDM) alternative native to Windows. Built in pure Go with native WebView2 Fluent Dark UI, multi-connection segment acceleration, intelligent queue scheduling, direct Telegram MTProto streaming, automatic channel scraping, and browser integration.

> *Developed and pair-programmed with **[Antigravity](https://antigravity.google)** — Google DeepMind's advanced agentic coding environment.*

---

## Key Features

1. **Native Windows Fluent GUI (Dark Theme)**
   - Powered by native Windows WebView2 (Microsoft Edge Chromium runtime) with butter-smooth 60fps animations.
   - 100% self-contained: compiles to a single native Windows 64-bit executable (`UDM.exe`) with embedded multi-resolution icon and zero console pop-up windows.

2. **High-Speed Multi-Connection Download Engine**
   - Concurrent byte-range downloads across configurable segments (4, 8, 16, 32 connections).
   - Direct-offset random access disk writes via `os.File.WriteAt` with pre-allocated file sizing — zero temporary file re-stitching or reassembly overhead.
   - Resumable checkpointing (`.dmstate`) with automatic forward-progress retry tracking.

3. **Direct Telegram MTProto Integration**
   - Connects straight to Telegram's distributed Data Centers (DC 1–5) via raw MTProto — bypasses the crippling speed limits (100–300 KB/s) of standard cloud bot APIs to achieve maximum ISP / 5G speeds (40+ MB/s).
   - Supports both public and restricted private channels (`https://t.me/c/...`).
   - 32-channel connection pool with asynchronous chunk prefetching and automatic 64-bit offset handling for files >2 GB.

4. **Queue Management & Disk Protection**
   - Configurable `MaxConcurrent` scheduler (runs active downloads in parallel; remaining jobs wait in queue).
   - Pause, Resume, Delete, Global "Pause All", and "Resume All".
   - Per-download custom destination path with native Windows folder picker dialog.
   - Dedicated global Temp folder (e.g. `D:\Temp`) to prevent primary OS drives (`C:\`) from running out of space during large transfers.

5. **Telegram Channel Media Scraper & Batch Enqueuer**
   - Inspects any public or private channel/group for videos, audio, documents, and archives.
   - Interactive table with file size, MIME type, message ID, and date.
   - 1-Click **"⚡ Download Selected"** batch downloader.

6. **Browser & System Integration**
   - **Clipboard Auto-Sniffer:** Monitors the Windows clipboard in the background for download URLs or Telegram links.
   - **Browser Bookmarklet:** 1-click browser bookmarklet to send any file link or tab directly to UDM.
   - **Local REST API:** Local endpoint (`http://127.0.0.1:8080/api/queue/add?url=...`) for external script automation.

---

## Project Structure

```text
UniversalDownloadManager/
├── cmd/
│   ├── gui/          # Native Windows WebView2 GUI desktop application (UDM.exe)
│   │   ├── app.ico   # Multi-resolution Windows icon (256, 128, 64, 48, 32, 16)
│   │   ├── app.rc    # Windows PE resource script
│   │   └── main.go   # Embedded HTTP bridge, GUI host, and queue driver
│   ├── dm/           # Standalone high-speed CLI download engine (dm.exe)
│   │   └── main.go
│   └── tgbridge/     # Standalone local MTProto-to-HTTP streaming server
│       └── main.go
├── pkg/
│   ├── downloader/   # Multi-part chunk engine, disk writes, progress reporting
│   ├── tgbridge/     # Telegram DC connection pool, auth, and HTTP range streaming
│   ├── queue/        # Download queue scheduler, settings persistence, file mover
│   ├── tgscraper/    # Channel media history crawler & document extractor
│   ├── clipboard/    # Win32 clipboard API listener
│   └── dialog/       # Native Windows folder browser dialog
├── LICENSE           # MIT License
└── README.md
```

---

## Prerequisites

- **OS:** Windows 10 or Windows 11 (64-bit).
- **Go:** [Go 1.22+](https://go.dev/dl/) or [Go 1.25+](https://go.dev/dl/).
- **WebView2:** Built into Windows 10 (recent updates) and Windows 11 by default. If missing, install the [Evergreen WebView2 Runtime](https://developer.microsoft.com/en-us/microsoft-edge/webview2/).
- **MinGW / GCC (Optional):** Required only if you want to rebuild the `.ico` resource file (`rsrc.syso`). A pre-compiled resource or binary can be built without it.
- **Telegram Account:** `api_id` and `api_hash` from [my.telegram.org](https://my.telegram.org) (only needed for Telegram features; direct HTTP downloads work out-of-the-box).

---

## Building from Source

Clone the repository:
```powershell
git clone https://github.com/akg-10/UniversalDownloadManager.git
cd UniversalDownloadManager
```

Download Go module dependencies:
```powershell
go mod tidy
```

### 1. Building the Desktop GUI (`UDM.exe`)

#### (Optional) Compile the Windows Icon Resource:
If you modified `cmd/gui/app.ico` or `cmd/gui/app.rc`:
```powershell
cd cmd\gui
windres.exe -i app.rc -O coff -o rsrc.syso
cd ..\..
```

#### Build the GUI Binary:
Use the `-H windowsgui` linker flag to compile a clean executable without a console window:
```powershell
go build -ldflags="-H windowsgui -s -w" -o UDM.exe ./cmd/gui
```

### 2. Building the Standalone CLI Engine (`dm.exe`)
```powershell
go build -ldflags="-s -w" -o dm.exe ./cmd/dm
```

### 3. Building the Standalone Telegram Bridge (`tgbridge.exe`)
```powershell
go build -ldflags="-s -w" -o tgbridge.exe ./cmd/tgbridge
```

---

## Configuration & Telegram Setup

Direct HTTP/HTTPS downloads require zero configuration.

To enable Telegram MTProto streaming:
1. Obtain an **API ID** and **API Hash** from [my.telegram.org](https://my.telegram.org) under *API development tools*.
2. Create the file `tg-data/config.json` in the application root directory:
   ```json
   {
     "api_id": 1234567,
     "api_hash": "0123456789abcdef0123456789abcdef",
     "phone": "+1234567890"
   }
   ```
3. On first run, UDM will request an authentication code (sent to your Telegram app) and your 2FA password (if enabled). The authorized session is securely persisted locally to `tg-data/session.json` (this file is excluded in `.gitignore` and never committed).

---

## Usage Guide & Features

### 1. Using the Desktop GUI (`UDM.exe`)

Simply run:
```powershell
.\UDM.exe
```

#### Adding a Download
- Click **`+ Add Download`** in the top navigation.
- **URL or Telegram Link:** Paste any HTTP/HTTPS direct link or Telegram link (e.g., `https://t.me/channel_name/42` or `https://t.me/c/4308160059/3`).
- **Save Location:** Click **Browse...** to pick any target folder (e.g. `E:\Movies`, `D:\Downloads`).
- **Concurrent Connections:** Choose 4, 8, 16, or 32 parts (default: 16).
- Click **Start Download**.

#### Managing Queues
- **Pause / Resume:** Click the pause/play icon next to any active download.
- **Delete:** Click the trash icon to remove the job and cleanup temporary state files.
- **Global Controls:** Use **Pause All** and **Resume All** to suspend or resume all active tasks simultaneously.
- **Settings:** Click **Settings** in the top bar to set the maximum number of concurrent downloads (e.g. 2 active at a time, rest queued) and change the global Temp download directory.

#### Telegram Channel Scraper
- Switch to the **Telegram Scraper** tab.
- Enter a channel username or link (e.g. `https://t.me/joinchat/...` or `https://t.me/MyChannel`).
- Click **Scrape Channel**.
- Browse through the list of media files, select the desired items, and click **⚡ Download Selected** to enqueue all of them at once.

#### Browser Integration (Clipboard & Bookmarklet)
- **Clipboard Monitor:** Enable the *Clipboard Monitor* toggle in the top bar. Any direct download URL or Telegram link copied to your Windows clipboard (`Ctrl + C`) will automatically open the Add Download prompt in UDM.
- **Bookmarklet:** In Settings, drag the **UDM Download** bookmarklet to your browser's bookmarks bar. Clicking it on any page will send the current page or media link straight to UDM.

---

### 2. Using the Standalone CLI Engine (`dm.exe`)

The CLI engine provides a clean, dynamic in-terminal progress dashboard:

```powershell
# Basic 16-connection download:
.\dm.exe "https://example.com/file.iso"

# Specify 32 concurrent connections:
.\dm.exe -c 32 "https://example.com/largefile.zip"

# Specify custom output path:
.\dm.exe -c 16 -o "E:\ISO\ubuntu.iso" "https://releases.ubuntu.com/.../ubuntu.iso"

# Download Telegram file via local bridge:
.\dm.exe -c 16 "http://127.0.0.1:8080/stream?link=https%3A%2F%2Ft.me%2Fc%2F4308160059%2F3"
```

#### CLI Flags:
| Flag | Description | Default |
| :--- | :--- | :--- |
| `-c` | Number of concurrent connections | `16` |
| `-o` | Output file path | Auto-detected from URL/headers |
| `-t` | Directory for temporary chunks | Current directory |
| `-h` | Show help and usage options | |

---

## Acknowledgements

Built and pair-programmed using **[Antigravity](https://antigravity.google)**, Google DeepMind's agentic AI coding assistant.

---

## License

This project is licensed under the [MIT License](LICENSE).
