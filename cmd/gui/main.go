package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"dm-engine/pkg/clipboard"
	"dm-engine/pkg/dialog"
	"dm-engine/pkg/queue"
	"dm-engine/pkg/tgbridge"
	"dm-engine/pkg/tgscraper"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/jchv/go-webview2"
)

//go:embed all:web
var webFS embed.FS

type AppConfig struct {
	APIID    int    `json:"api_id"`
	APIHash  string `json:"api_hash"`
	BotToken string `json:"bot_token,omitempty"`
}

func loadAppConfig(path string) AppConfig {
	var cfg AppConfig
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	return cfg
}

func main() {
	portFlag := flag.Int("port", 8080, "Port for internal service")
	dataDirFlag := flag.String("data-dir", "./tg-data", "Data directory")
	flag.Parse()

	_ = os.MkdirAll(*dataDirFlag, 0700)
	configPath := filepath.Join(*dataDirFlag, "config.json")
	savedCfg := loadAppConfig(configPath)

	apiID := savedCfg.APIID
	apiHash := savedCfg.APIHash

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		cancel()
	}()

	// Clipboard queue for frontend pop
	var clipMu sync.Mutex
	var pendingClipboardLink string
	onClipLink := func(link string) {
		clipMu.Lock()
		pendingClipboardLink = link
		clipMu.Unlock()
	}

	// Initialize Queue Manager
	queueMgr := queue.NewManager(*dataDirFlag, *portFlag, nil)
	defer queueMgr.Stop()

	// Start Clipboard Watcher
	go clipboard.Watch(ctx, onClipLink)

	// Launch Telegram Client in background if credentials exist
	var tgClient *telegram.Client
	var tgRaw *tg.Client
	var scraper *tgscraper.ChannelScraper
	var resolver *tgbridge.Resolver
	var dcClients = make(map[int]*tg.Client)
	var dcMu sync.RWMutex

	getDCClient := func(ctx context.Context, dcID int) (*tg.Client, error) {
		if tgClient == nil {
			return nil, fmt.Errorf("telegram client not active")
		}
		dcMu.RLock()
		c, ok := dcClients[dcID]
		dcMu.RUnlock()
		if ok {
			return c, nil
		}

		dcMu.Lock()
		defer dcMu.Unlock()
		if c, ok := dcClients[dcID]; ok {
			return c, nil
		}

		fmt.Printf("[+] Establishing connection to Telegram Media DC %d...\n", dcID)
		invoker, err := tgClient.DC(ctx, dcID, 32)
		if err != nil {
			invoker, err = tgClient.MediaOnly(ctx, dcID, 32)
			if err != nil {
				return nil, err
			}
		}
		c = tg.NewClient(invoker)
		dcClients[dcID] = c
		return c, nil
	}

	if apiID != 0 && apiHash != "" {
		sessionPath := filepath.Join(*dataDirFlag, "session.json")
		tgClient = telegram.NewClient(apiID, apiHash, telegram.Options{
			SessionStorage: &session.FileStorage{Path: sessionPath},
			NoUpdates:      true,
		})

		go func() {
			_ = tgClient.Run(ctx, func(cCtx context.Context) error {
				pool, err := tgClient.Pool(32)
				if err == nil {
					tgRaw = tg.NewClient(pool)
				} else {
					tgRaw = tgClient.API()
				}
				resolver = tgbridge.NewResolver(tgClient.API())
				_ = resolver.PopulateDialogs(cCtx)
				scraper = tgscraper.NewChannelScraper(tgClient.API())

				user, err := tgClient.Self(cCtx)
				if err == nil {
					fmt.Printf("[+] Telegram connected as %s (@%s)\n", user.FirstName, user.Username)
				}

				<-cCtx.Done()
				return nil
			})
		}()
	}

	// Setup HTTP Handlers
	mux := http.NewServeMux()

	// 1. Embedded UI
	distFS, err := fs.Sub(webFS, "web")
	if err == nil {
		mux.Handle("/", http.FileServer(http.FS(distFS)))
	}

	// 2. Queue API
	mux.HandleFunc("/api/queue/list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(queueMgr.GetItems())
	})

	mux.HandleFunc("/api/queue/add", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		var req struct {
			URL      string `json:"url"`
			Filename string `json:"filename"`
			SaveDir  string `json:"save_dir"`
			Parts    int    `json:"parts"`
		}

		if r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&req)
		} else {
			req.URL = r.URL.Query().Get("url")
			req.Filename = r.URL.Query().Get("filename")
			req.SaveDir = r.URL.Query().Get("save_dir")
		}

		if req.URL == "" {
			http.Error(w, `{"error": "url required"}`, http.StatusBadRequest)
			return
		}

		item, err := queueMgr.AddItem(req.URL, req.Filename, req.SaveDir, req.Parts)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "%s"}`, err.Error()), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(item)
	})

	mux.HandleFunc("/api/dialog/select-folder", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		folder, err := dialog.SelectFolder()
		if err != nil {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"path": folder})
	})

	mux.HandleFunc("/api/queue/pause", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		_ = queueMgr.PauseItem(id)
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/api/queue/resume", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		_ = queueMgr.ResumeItem(id)
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/api/queue/delete", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		_ = queueMgr.DeleteItem(id, false)
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/api/queue/pause-all", func(w http.ResponseWriter, r *http.Request) {
		queueMgr.PauseAll()
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/api/queue/resume-all", func(w http.ResponseWriter, r *http.Request) {
		queueMgr.ResumeAll()
		w.WriteHeader(http.StatusOK)
	})

	// 3. Settings API
	mux.HandleFunc("/api/settings/get", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(queueMgr.GetSettings())
	})

	mux.HandleFunc("/api/settings/save", func(w http.ResponseWriter, r *http.Request) {
		var s queue.Settings
		_ = json.NewDecoder(r.Body).Decode(&s)
		_ = queueMgr.UpdateSettings(s)
		w.WriteHeader(http.StatusOK)
	})

	// 4. Scraper API
	mux.HandleFunc("/api/scraper/scan", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if scraper == nil {
			http.Error(w, `{"error": "Telegram client is connecting or not configured"}`, http.StatusServiceUnavailable)
			return
		}

		chURL := r.URL.Query().Get("url")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 100
		}

		items, err := scraper.ScrapeChannel(r.Context(), chURL, limit)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(items)
	})

	// 5. Clipboard Pop API
	mux.HandleFunc("/api/clipboard/pop", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		clipMu.Lock()
		defer clipMu.Unlock()
		if pendingClipboardLink != "" {
			link := pendingClipboardLink
			pendingClipboardLink = ""
			_ = json.NewEncoder(w).Encode(map[string]string{"url": link})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{})
	})

	// 6. Telegram Streaming API
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		targetLink := r.URL.Query().Get("link")
		if resolver == nil || tgRaw == nil {
			http.Error(w, "telegram client initializing", http.StatusServiceUnavailable)
			return
		}

		meta, err := resolver.ResolveLink(r.Context(), targetLink)
		if err != nil {
			http.Error(w, fmt.Sprintf("resolve error: %v", err), http.StatusNotFound)
			return
		}

		tgbridge.StreamTelegramFile(r.Context(), tgRaw, getDCClient, meta, w, r)
	})

	server := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", *portFlag),
		Handler: mux,
	}

	go func() {
		_ = server.ListenAndServe()
	}()

	// Wait 250ms for server to bind
	time.Sleep(250 * time.Millisecond)

	appURL := fmt.Sprintf("http://127.0.0.1:%d", *portFlag)
	fmt.Printf("[+] Antigravity Download Manager GUI running at %s\n", appURL)

	// Launch Native Windows WebView2 Desktop Window
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  "Antigravity Download Manager",
			Width:  1120,
			Height: 740,
			IconId: 2,
			Center: true,
		},
	})

	if w == nil {
		fmt.Printf("[-] Failed to create native window. Access the GUI via your browser at %s\n", appURL)
		<-ctx.Done()
		return
	}

	defer w.Destroy()
	w.SetSize(1120, 740, webview2.HintNone)
	w.Navigate(appURL)
	w.Run()
}
