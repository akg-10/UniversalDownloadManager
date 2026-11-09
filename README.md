# Universal Download Manager (UDM)

[![Built with Antigravity](https://img.shields.io/badge/Built%20with-Antigravity-4285F4?style=flat-square&logo=google)](https://github.com/google-deepmind)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A high-performance personal Internet Download Manager alternative native to Windows, featuring multi-connection acceleration, queue scheduling, Telegram MTProto streaming, automatic channel scraping, and browser integration.

> *Developed with **[Antigravity](https://antigravity.google)** — Google DeepMind's advanced agentic coding environment.*

---

## Features

1. **Native Windows Fluent GUI (Dark Theme)**
   * Powered by the native Windows WebView2 runtime (Edge Chromium engine).
   * Butter-smooth 60fps animations, responsive layout, dark theme.
   * Compiles to a single standalone Windows executable `UDM.exe`.

2. **Multi-Connection Segmented Download Engine**
   * Concurrent byte-range downloads (4, 8, 16, 32 connections).
   * Direct-offset random access disk writes (`os.File.WriteAt`) with zero-reassembly overhead.
   * Checkpointing (`.dmstate`) with forward-progress retry tracking.

3. **Telegram MTProto Integration**
   * Connects directly to Telegram's Data Centers via MTProto without third-party cloud bot bottlenecks.
   * Automatic Data Center migration (seamlessly redirects to DC 1, DC 2, DC 4, etc.).
   * 32-channel connection pool with asynchronous chunk prefetching.

4. **Queue Management & Storage Control**
   * Max concurrent downloads scheduler (active downloads run, remaining wait in queue).
   * Pause, Resume, Delete, Pause All, Resume All.
   * Custom Save Location per download with native Windows **Browse...** folder picker.
   * Dedicated Temp Directory support to protect main OS drives from fragmentation.

5. **Channel Media Scraper & Batch Downloader**
   * Scans any Telegram channel for movies, videos, documents, and archives.
   * 1-Click **"⚡ Download Selected"** batch enqueuing.

6. **Browser Integration**
   * Background Windows Clipboard Auto-Sniffer.
   * 1-Click Drag-and-Drop Browser Bookmarklet.
   * Local REST API (`/api/queue/add?url=...`).

---

## How to Build & Run

### Running the Desktop GUI:
```powershell
cd E:\Code\DM
.\UDM.exe
```

### Running the CLI Engine:
```powershell
.\dm.exe -c 16 "https://example.com/largefile.zip"
```

---

## Acknowledgements

Built and pair-programmed using **Antigravity**, Google DeepMind's agentic AI coding assistant.

---

## License

This project is licensed under the [MIT License](LICENSE).
