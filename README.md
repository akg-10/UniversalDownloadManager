# Antigravity Download Manager (AntigravityDM)

A high-performance personal Internet Download Manager alternative native to Windows, with unified multi-part downloading, queue management, Telegram MTProto integration, automatic channel scraping, and browser integration.

---

## What is Inside `AntigravityDM.exe` (All-in-One Desktop App)

Everything has been consolidated into a **single, standalone Windows application** (`E:\Code\DM\AntigravityDM.exe`):

1. **Native Windows Fluent GUI (Dark Theme)**
   * Powered by the native Windows WebView2 runtime (Edge Chromium engine).
   * Butter-smooth 60fps animations, responsive layout, dark theme.
   * Runs as a true native desktop application without background command prompts.

2. **Full Queue Management**
   * Add, Pause, Resume, and Delete individual downloads or use **Pause All / Resume All**.
   * Concurrency scheduler: keeps up to $N$ active downloads (default: 3) while remaining items wait in queue.
   * Checkpointed state: interrupted downloads resume from the exact byte on app restart.

3. **Storage & Temp Location Control (Settings)**
   * **Download Directory:** Configure where finished files are stored.
   * **Temporary Directory (Temp):** Configure where active partial segments are written during download (supports dedicated fast SSDs or scratch drives).
   * Automatic cross-drive file relocation on download completion.

4. **Telegram Channel Media Scraper & Batch Downloader**
   * Paste any Telegram channel link (e.g. `https://t.me/c/4308160059` or `https://t.me/channelname`).
   * Scans channel messages using MTProto and displays all media (movies, archives, software, documents) in a searchable table.
   * 1-Click **"⚡ Download Selected"**: Batch-enqueues selected files into the download manager automatically!

5. **Browser & Clipboard Integration**
   * **Clipboard Auto-Sniffer:** When you copy any download URL or Telegram link in Windows, the app automatically detects it and prompts you to start the download.
   * **1-Click Bookmarklet:** Drag the bookmarklet from Settings into Chrome/Edge/Brave to send any webpage's file directly to Antigravity DM with one click.
   * **REST API:** Listens on `http://127.0.0.1:8080/api/queue/add?url=...` for browser extensions.

---

## How to Run

Simply double-click or run from PowerShell:
```powershell
cd E:\Code\DM
.\AntigravityDM.exe
```

The desktop app window will immediately launch!
