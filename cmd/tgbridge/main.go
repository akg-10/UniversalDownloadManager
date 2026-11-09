package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"dm-engine/pkg/tgbridge"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

type terminalAuth struct{}

func (terminalAuth) Phone(ctx context.Context) (string, error) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("\n[?] Enter phone number with country code (e.g. +1234567890): ")
	phone, _ := reader.ReadString('\n')
	return strings.TrimSpace(phone), nil
}

func (terminalAuth) Password(ctx context.Context) (string, error) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("[?] Enter 2FA cloud password (press Enter if none): ")
	password, _ := reader.ReadString('\n')
	return strings.TrimSpace(password), nil
}

func (terminalAuth) AcceptTermsOfService(ctx context.Context, tos tg.HelpTermsOfService) error {
	return nil
}

func (terminalAuth) SignUp(ctx context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("account sign-up is not supported via CLI")
}

func (terminalAuth) Code(ctx context.Context, sentCode *tg.AuthSentCode) (string, error) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("[?] Enter the login code received in Telegram: ")
	code, _ := reader.ReadString('\n')
	return strings.TrimSpace(code), nil
}

type BridgeConfig struct {
	APIID    int    `json:"api_id"`
	APIHash  string `json:"api_hash"`
	BotToken string `json:"bot_token,omitempty"`
}

func loadConfig(configPath string) BridgeConfig {
	var cfg BridgeConfig
	data, err := os.ReadFile(configPath)
	if err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	return cfg
}

func saveConfig(configPath string, cfg BridgeConfig) {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err == nil {
		_ = os.WriteFile(configPath, data, 0600)
	}
}

func getEnvAny(keys ...string) string {
	for _, k := range keys {
		if val := strings.TrimSpace(os.Getenv(k)); val != "" {
			return val
		}
	}
	return ""
}

func main() {
	apiIDFlag := flag.Int("api-id", 0, "Telegram API ID (from my.telegram.org)")
	apiHashFlag := flag.String("api-hash", "", "Telegram API Hash (from my.telegram.org)")
	botTokenFlag := flag.String("bot-token", "", "Optional Telegram Bot Token")
	portFlag := flag.Int("port", 8080, "Port for local HTTP streaming server")
	dataDirFlag := flag.String("data-dir", "./tg-data", "Directory to store session and config")
	flag.Parse()

	_ = os.MkdirAll(*dataDirFlag, 0700)
	configPath := filepath.Join(*dataDirFlag, "config.json")
	savedCfg := loadConfig(configPath)

	apiID := *apiIDFlag
	if apiID == 0 {
		if envID := getEnvAny("TG_API_ID", "TELEGRAM_API_ID", "API_ID", "APP_ID"); envID != "" {
			apiID, _ = strconv.Atoi(envID)
		} else if savedCfg.APIID != 0 {
			apiID = savedCfg.APIID
		}
	}

	apiHash := *apiHashFlag
	if apiHash == "" {
		if envHash := getEnvAny("TG_API_HASH", "TELEGRAM_API_HASH", "API_HASH", "APP_HASH"); envHash != "" {
			apiHash = envHash
		} else if savedCfg.APIHash != "" {
			apiHash = savedCfg.APIHash
		}
	}

	botToken := *botTokenFlag
	if botToken == "" {
		if envToken := getEnvAny("TG_BOT_TOKEN", "TELEGRAM_BOT_TOKEN", "BOT_TOKEN"); envToken != "" {
			botToken = envToken
		} else if savedCfg.BotToken != "" {
			botToken = savedCfg.BotToken
		}
	}

	fmt.Println("==========================================================")
	fmt.Println("  Telegram Local Stream Bridge (MTProto -> HTTP 206)")
	fmt.Println("==========================================================")

	reader := bufio.NewReader(os.Stdin)
	var newlyEntered bool

	if apiID == 0 {
		fmt.Print("\n[?] Enter Telegram API ID (from my.telegram.org): ")
		input, _ := reader.ReadString('\n')
		apiID, _ = strconv.Atoi(strings.TrimSpace(input))
		newlyEntered = true
	}

	if apiHash == "" {
		fmt.Print("[?] Enter Telegram API Hash: ")
		input, _ := reader.ReadString('\n')
		apiHash = strings.TrimSpace(input)
		newlyEntered = true
	}

	if apiID == 0 || apiHash == "" {
		fmt.Println("[-] Error: Telegram API ID and API Hash are required.")
		os.Exit(1)
	}

	// Persist to config.json so user never has to enter again
	if newlyEntered || savedCfg.APIID != apiID || savedCfg.APIHash != apiHash {
		saveConfig(configPath, BridgeConfig{
			APIID:    apiID,
			APIHash:  apiHash,
			BotToken: botToken,
		})
		fmt.Printf("[+] Credentials saved to %s (you won't be asked again).\n", configPath)
	}

	sessionPath := filepath.Join(*dataDirFlag, "session.json")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n[*] Shutting down Telegram bridge...")
		cancel()
	}()

	client := telegram.NewClient(apiID, apiHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: sessionPath},
		NoUpdates:      true,
	})

	err := client.Run(ctx, func(ctx context.Context) error {
		var authErr error
		if botToken != "" {
			fmt.Println("[*] Authenticating with Bot Token...")
			_, authErr = client.Auth().Bot(ctx, botToken)
		} else {
			status, err := client.Auth().Status(ctx)
			if err != nil {
				return fmt.Errorf("failed to check auth status: %w", err)
			}

			if !status.Authorized {
				fmt.Println("[*] No existing session found. Starting login...")
				flow := auth.NewFlow(terminalAuth{}, auth.SendCodeOptions{})
				authErr = client.Auth().IfNecessary(ctx, flow)
			} else {
				fmt.Println("[+] Existing Telegram session restored.")
			}
		}

		if authErr != nil {
			return fmt.Errorf("authentication failed: %w", authErr)
		}

		user, err := client.Self(ctx)
		if err == nil {
			fmt.Printf("[+] Logged in as: %s %s (@%s)\n", user.FirstName, user.LastName, user.Username)
		}

		bridgeServer := tgbridge.NewServer(*portFlag, client)
		return bridgeServer.Start(ctx)
	})

	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Printf("[-] Telegram bridge stopped with error: %v\n", err)
		os.Exit(1)
	}
}
