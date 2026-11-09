package tgbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

// Server provides the local HTTP server bridging Telegram MTProto to HTTP clients.
type Server struct {
	port      int
	client    *telegram.Client
	raw       *tg.Client
	poolRaw   *tg.Client
	resolver  *Resolver
	dcClients map[int]*tg.Client
	dcMu      sync.RWMutex
}

// NewServer creates a new local bridge server instance with connection pooling.
func NewServer(port int, client *telegram.Client) *Server {
	pool, err := client.Pool(32)
	var poolRaw *tg.Client
	if err == nil {
		poolRaw = tg.NewClient(pool)
		fmt.Println("[+] Multi-connection MTProto pool initialized (32 parallel channels).")
	} else {
		poolRaw = client.API()
	}

	return &Server{
		port:      port,
		client:    client,
		raw:       client.API(),
		poolRaw:   poolRaw,
		resolver:  NewResolver(client.API()),
		dcClients: make(map[int]*tg.Client),
	}
}

// GetDCClient returns a multi-socket client connected directly to the requested Telegram DC.
func (s *Server) GetDCClient(ctx context.Context, dcID int) (*tg.Client, error) {
	s.dcMu.RLock()
	c, ok := s.dcClients[dcID]
	s.dcMu.RUnlock()
	if ok {
		return c, nil
	}

	s.dcMu.Lock()
	defer s.dcMu.Unlock()

	if c, ok := s.dcClients[dcID]; ok {
		return c, nil
	}

	fmt.Printf("[*] Connecting to Telegram Media DC %d...\n", dcID)
	invoker, err := s.client.DC(ctx, dcID, 32)
	if err != nil {
		invoker, err = s.client.MediaOnly(ctx, dcID, 32)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to DC %d: %w", dcID, err)
		}
	}

	c = tg.NewClient(invoker)
	s.dcClients[dcID] = c
	fmt.Printf("[+] Successfully connected to Telegram DC %d with 32 parallel sockets!\n", dcID)
	return c, nil
}

// Start launches the HTTP listener and serves both the web UI and streaming API.
func (s *Server) Start(ctx context.Context) error {
	_ = s.resolver.PopulateDialogs(ctx)

	mux := http.NewServeMux()

	// 1. Web Dashboard
	mux.HandleFunc("/", s.handleDashboard)

	// 2. Metadata API
	mux.HandleFunc("/api/info", s.handleInfo)

	// 3. Streaming Endpoint with HTTP Range support
	mux.HandleFunc("/stream", s.handleStream)

	// 4. Direct 16-thread MTProto Downloader
	mux.HandleFunc("/api/direct-download", s.handleDirectDownload)

	server := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", s.port),
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	fmt.Printf("[+] Local Telegram Bridge listening on http://127.0.0.1:%d\n", s.port)
	fmt.Printf("    Open http://127.0.0.1:%d in your browser to inspect files and generate links.\n", s.port)

	return server.ListenAndServe()
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	targetLink := r.URL.Query().Get("link")
	if targetLink == "" {
		http.Error(w, `{"error": "missing 'link' query parameter"}`, http.StatusBadRequest)
		return
	}

	meta, err := s.resolver.ResolveLink(r.Context(), targetLink)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	_ = json.NewEncoder(w).Encode(meta)
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	targetLink := r.URL.Query().Get("link")
	if targetLink == "" {
		http.Error(w, "missing link parameter", http.StatusBadRequest)
		return
	}

	meta, err := s.resolver.ResolveLink(r.Context(), targetLink)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to resolve telegram link: %v", err), http.StatusNotFound)
		return
	}

	// Stream with DC auto-migration support
	StreamTelegramFile(r.Context(), s.poolRaw, s.GetDCClient, meta, w, r)
}

func (s *Server) handleDirectDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	targetLink := r.URL.Query().Get("link")
	meta, err := s.resolver.ResolveLink(r.Context(), targetLink)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	outPath := filepath.Join(".", meta.Filename)
	go func() {
		fmt.Printf("\n[*] Starting direct 16-thread MTProto download for: %s\n", meta.Filename)
		startTime := time.Now()
		_, err := s.client.Download(meta.Location).WithThreads(16).ToPath(context.Background(), outPath)
		if err != nil {
			fmt.Printf("[-] Direct download error: %v\n", err)
		} else {
			duration := time.Since(startTime)
			speed := float64(meta.Size) / duration.Seconds() / (1024 * 1024)
			fmt.Printf("\n[+] Direct download complete: %s in %s (%.2f MB/s)!\n", outPath, duration.Round(time.Second), speed)
		}
	}()

	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":   "started",
		"filename": meta.Filename,
		"path":     outPath,
	})
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(dashboardHTML))
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Telegram Local Download Bridge</title>
  <style>
    :root {
      --bg: #0f172a;
      --card: #1e293b;
      --border: #334155;
      --text: #f8fafc;
      --muted: #94a3b8;
      --accent: #38bdf8;
      --accent-hover: #0ea5e9;
      --success: #22c55e;
      --btn-direct: #10b981;
      --btn-direct-hover: #059669;
    }
    * { box-sizing: border-box; margin: 0; padding: 0; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; }
    body { background-color: var(--bg); color: var(--text); min-height: 100vh; display: flex; flex-direction: column; align-items: center; padding: 2rem 1rem; }
    .container { max-width: 680px; width: 100%; }
    .header { text-align: center; margin-bottom: 2rem; }
    .header h1 { font-size: 1.75rem; font-weight: 700; margin-bottom: 0.5rem; display: flex; align-items: center; justify-content: center; gap: 0.5rem; }
    .badge { display: inline-flex; align-items: center; gap: 0.4rem; padding: 0.35rem 0.75rem; border-radius: 9999px; font-size: 0.8rem; background: rgba(34, 197, 94, 0.15); color: var(--success); border: 1px solid rgba(34, 197, 94, 0.3); }
    .badge-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--success); }
    .card { background: var(--card); border: 1px solid var(--border); border-radius: 12px; padding: 1.5rem; margin-bottom: 1.5rem; box-shadow: 0 4px 6px -1px rgba(0, 0, 0, 0.2); }
    .input-group { display: flex; gap: 0.5rem; margin-top: 1rem; }
    input[type="text"] { flex: 1; background: #0b1120; border: 1px solid var(--border); border-radius: 8px; color: #fff; padding: 0.75rem 1rem; font-size: 0.95rem; outline: none; transition: border-color 0.2s; }
    input[type="text"]:focus { border-color: var(--accent); }
    button { background: var(--accent); color: #0f172a; border: none; border-radius: 8px; padding: 0.75rem 1.25rem; font-weight: 600; cursor: pointer; transition: background 0.2s; white-space: nowrap; }
    button:hover { background: var(--accent-hover); }
    .btn-direct { background: var(--btn-direct); color: #fff; margin-top: 0.75rem; width: 100%; padding: 0.85rem; font-size: 0.95rem; }
    .btn-direct:hover { background: var(--btn-direct-hover); }
    .result-box { display: none; margin-top: 1.5rem; border-top: 1px solid var(--border); padding-top: 1.5rem; }
    .meta-row { display: flex; justify-content: space-between; margin-bottom: 0.6rem; font-size: 0.9rem; }
    .meta-label { color: var(--muted); }
    .meta-value { font-weight: 600; word-break: break-all; }
    .command-box { background: #0b1120; border: 1px solid var(--border); border-radius: 8px; padding: 1rem; margin-top: 1rem; position: relative; }
    .command-code { font-family: "Cascadia Code", Consolas, monospace; font-size: 0.85rem; color: #38bdf8; word-break: break-all; }
    .copy-btn { position: absolute; top: 0.5rem; right: 0.5rem; padding: 0.35rem 0.65rem; font-size: 0.75rem; background: var(--border); color: #fff; }
    .copy-btn:hover { background: #475569; }
    .loader { display: none; text-align: center; margin: 1rem 0; color: var(--accent); font-size: 0.9rem; }
    .error-msg { display: none; color: #ef4444; background: rgba(239, 68, 68, 0.1); border: 1px solid rgba(239, 68, 68, 0.2); border-radius: 8px; padding: 0.75rem; margin-top: 1rem; font-size: 0.9rem; }
    .success-msg { display: none; color: #22c55e; background: rgba(34, 197, 94, 0.1); border: 1px solid rgba(34, 197, 94, 0.2); border-radius: 8px; padding: 0.75rem; margin-top: 1rem; font-size: 0.9rem; }
  </style>
</head>
<body>
  <div class="container">
    <div class="header">
      <h1>⚡ Telegram Local Bridge</h1>
      <div class="badge"><span class="badge-dot"></span> MTProto Multi-Socket Pool Active</div>
    </div>

    <div class="card">
      <h2 style="font-size: 1.1rem; margin-bottom: 0.35rem;">Generate High-Speed Download Link</h2>
      <p style="font-size: 0.85rem; color: var(--muted);">Paste any Telegram post link (e.g. <code>https://t.me/c/12345/678</code> or <code>https://t.me/channel/123</code>)</p>
      
      <div class="input-group">
        <input type="text" id="tgLink" placeholder="https://t.me/..." autofocus>
        <button id="resolveBtn" onclick="resolveLink()">Inspect File</button>
      </div>

      <div class="loader" id="loader">Resolving Telegram MTProto metadata...</div>
      <div class="error-msg" id="errorMsg"></div>
      <div class="success-msg" id="successMsg"></div>

      <div class="result-box" id="resultBox">
        <div class="meta-row"><span class="meta-label">File Name:</span><span class="meta-value" id="resFilename">-</span></div>
        <div class="meta-row"><span class="meta-label">File Size:</span><span class="meta-value" id="resSize">-</span></div>
        <div class="meta-row"><span class="meta-label">MIME Type:</span><span class="meta-value" id="resMime">-</span></div>

        <button class="btn-direct" id="directBtn" onclick="startDirectDownload()">⚡ Download Directly with MTProto (16 Threads)</button>

        <p style="font-size: 0.85rem; color: var(--muted); margin-top: 1.25rem;">Or use <code>dm.exe</code> (8 parallel streams):</p>
        <div class="command-box">
          <div class="command-code" id="cmdCode"></div>
          <button class="copy-btn" onclick="copyCommand()">Copy Command</button>
        </div>
      </div>
    </div>
  </div>

  <script>
    let currentLink = '';

    async function resolveLink() {
      const link = document.getElementById('tgLink').value.trim();
      currentLink = link;
      const loader = document.getElementById('loader');
      const errBox = document.getElementById('errorMsg');
      const sucBox = document.getElementById('successMsg');
      const resBox = document.getElementById('resultBox');

      if (!link) return;
      loader.style.display = 'block';
      errBox.style.display = 'none';
      sucBox.style.display = 'none';
      resBox.style.display = 'none';

      try {
        const resp = await fetch('/api/info?link=' + encodeURIComponent(link));
        const data = await resp.json();
        loader.style.display = 'none';

        if (!resp.ok || data.error) {
          errBox.textContent = data.error || 'Failed to inspect link.';
          errBox.style.display = 'block';
          return;
        }

        document.getElementById('resFilename').textContent = data.filename;
        document.getElementById('resSize').textContent = formatBytes(data.size);
        document.getElementById('resMime').textContent = data.mime_type || 'application/octet-stream';

        const streamUrl = window.location.origin + '/stream?link=' + encodeURIComponent(link);
        const cliCommand = '.\\dm.exe -c 8 "' + streamUrl + '"';
        document.getElementById('cmdCode').textContent = cliCommand;

        resBox.style.display = 'block';
      } catch (e) {
        loader.style.display = 'none';
        errBox.textContent = 'Server connection failed: ' + e.message;
        errBox.style.display = 'block';
      }
    }

    async function startDirectDownload() {
      if (!currentLink) return;
      const sucBox = document.getElementById('successMsg');
      const errBox = document.getElementById('errorMsg');
      sucBox.style.display = 'none';
      errBox.style.display = 'none';

      try {
        const resp = await fetch('/api/direct-download?link=' + encodeURIComponent(currentLink));
        const data = await resp.json();
        if (resp.ok && data.status === 'started') {
          sucBox.innerHTML = '🚀 <b>Direct download started in background!</b> Check your tgbridge terminal window for live progress and speed.';
          sucBox.style.display = 'block';
        } else {
          errBox.textContent = data.error || 'Failed to start direct download.';
          errBox.style.display = 'block';
        }
      } catch (e) {
        errBox.textContent = 'Request failed: ' + e.message;
        errBox.style.display = 'block';
      }
    }

    function copyCommand() {
      const cmd = document.getElementById('cmdCode').textContent;
      navigator.clipboard.writeText(cmd);
      alert('Copied to clipboard! Paste it into PowerShell.');
    }

    function formatBytes(bytes) {
      if (!bytes || bytes <= 0) return '0 B';
      const k = 1024;
      const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
      const i = Math.floor(Math.log(bytes) / Math.log(k));
      return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
    }
  </script>
</body>
</html>`
