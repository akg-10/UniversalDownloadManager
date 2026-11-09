package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"dm-engine/pkg/downloader"
)

func main() {
	concurrency := flag.Int("c", 8, "Number of concurrent download connections (parts)")
	outputDir := flag.String("d", ".", "Output directory for the downloaded file")
	outputFile := flag.String("o", "", "Custom destination filename")
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		printUsage()
		os.Exit(1)
	}

	targetURL := args[0]

	// Setup graceful interruption handling (Ctrl+C for Pause/Resume)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Printf("\n\n[!] Interrupt signal received. Pausing download and saving state...\n")
		cancel()
	}()

	cfg := downloader.DefaultConfig()
	cfg.Concurrency = *concurrency
	cfg.OutputDir = *outputDir
	cfg.Filename = *outputFile

	dl := downloader.New(cfg)

	fmt.Println("==========================================================")
	fmt.Println("  Antigravity Download Manager (Engine v1.0 - Native Win64)")
	fmt.Println("==========================================================")
	fmt.Printf("[*] Probing target URL...\n    %s\n", targetURL)

	info, err := downloader.ProbeURL(targetURL, cfg.UserAgent)
	if err != nil {
		fmt.Printf("[-] Failed to probe URL: %v\n", err)
		os.Exit(1)
	}

	finalFilename := *outputFile
	if finalFilename == "" {
		finalFilename = info.Filename
	}
	destPath := filepath.Join(*outputDir, finalFilename)

	resumableStr := "No (Single connection fallback)"
	if info.AcceptRanges {
		resumableStr = fmt.Sprintf("Yes (%d concurrent connections)", *concurrency)
	}

	fmt.Printf("\n[+] Target Metadata:\n")
	fmt.Printf("    • File Name   : %s\n", finalFilename)
	fmt.Printf("    • File Size   : %s (%d bytes)\n", formatBytes(info.TotalSize), info.TotalSize)
	fmt.Printf("    • Multi-Part  : %s\n", resumableStr)
	fmt.Printf("    • Destination : %s\n", destPath)

	if downloader.HasState(destPath) {
		fmt.Println("    • State       : Resuming from previous session checkpoint (.dmstate)")
	}

	fmt.Println("\n[*] Initiating download...")

	// Hide cursor on supporting terminals
	fmt.Print("\033[?25l")
	defer fmt.Print("\033[?25h") // restore cursor on exit

	lastLinesRendered := 0

	progressCallback := func(p downloader.ProgressInfo) {
		renderUI(&lastLinesRendered, p)
	}

	startTime := time.Now()
	state, err := dl.Download(ctx, targetURL, progressCallback)

	// Clean rendering lines
	if lastLinesRendered > 0 {
		fmt.Printf("\033[%dA\033[J", lastLinesRendered)
	}

	if err != nil {
		if ctx.Err() != nil {
			fmt.Printf("\n[v] Download paused successfully!\n")
			fmt.Printf("    State saved to: %s\n", downloader.StateFilePath(destPath))
			fmt.Printf("    To resume, simply run the same command again.\n\n")
			os.Exit(0)
		}
		fmt.Printf("\n[-] Download error: %v\n", err)
		os.Exit(1)
	}

	duration := time.Since(startTime)
	avgSpeed := 0.0
	if duration.Seconds() > 0 && state.TotalSize > 0 {
		avgSpeed = float64(state.TotalSize) / duration.Seconds()
	}

	fmt.Printf("\n==========================================================\n")
	fmt.Printf("[+] Download Complete!\n")
	fmt.Printf("    • Saved to  : %s\n", state.Destination)
	fmt.Printf("    • Size      : %s\n", formatBytes(state.TotalSize))
	fmt.Printf("    • Time Taken: %s\n", duration.Round(time.Millisecond))
	fmt.Printf("    • Avg Speed : %s/s\n", formatBytes(int64(avgSpeed)))
	fmt.Printf("==========================================================\n")
}

func renderUI(lastLines *int, p downloader.ProgressInfo) {
	// Erase previous lines
	if *lastLines > 0 {
		fmt.Printf("\033[%dA\033[J", *lastLines)
	}

	var lines []string

	// Overall Progress Bar
	barWidth := 30
	overallBar := renderBar(p.Percentage, barWidth)
	etaStr := "--:--"
	if p.ETA > 0 {
		etaStr = formatDuration(p.ETA)
	}

	lines = append(lines, fmt.Sprintf("Overall: [%s] %5.1f%% | %s / %s",
		overallBar, p.Percentage, formatBytes(p.DownloadedBytes), formatBytes(p.TotalSize)))
	lines = append(lines, fmt.Sprintf("Speed  : %s/s | ETA: %s",
		formatBytes(int64(p.SpeedBps)), etaStr))
	lines = append(lines, "")

	// Per-chunk Progress Bars
	if len(p.Chunks) > 1 {
		chunkBarWidth := 20
		for _, c := range p.Chunks {
			cBar := renderBar(c.Percentage, chunkBarWidth)
			status := "Idle"
			if c.Completed {
				status = "Done"
			} else if c.Active {
				status = "Active"
			}

			lines = append(lines, fmt.Sprintf("  Part %02d: [%s] %5.1f%% (%s / %s) [%s]",
				c.Index+1, cBar, c.Percentage, formatBytes(c.Downloaded), formatBytes(c.Total), status))
		}
	}

	output := strings.Join(lines, "\n") + "\n"
	fmt.Print(output)
	*lastLines = len(lines)
}

func renderBar(percentage float64, width int) string {
	if percentage < 0 {
		percentage = 0
	}
	if percentage > 100 {
		percentage = 100
	}

	filled := int(math.Round((percentage / 100.0) * float64(width)))
	if filled > width {
		filled = width
	}

	bar := strings.Repeat("=", filled)
	if filled < width && filled > 0 {
		bar = bar[:filled-1] + ">"
	}
	empty := strings.Repeat("-", width-len(bar))
	return bar + empty
}

func formatBytes(bytes int64) string {
	if bytes < 0 {
		return "Unknown"
	}
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second

	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

func printUsage() {
	fmt.Println("Usage: dm [options] <URL>")
	fmt.Println("\nOptions:")
	flag.PrintDefaults()
	fmt.Println("\nExamples:")
	fmt.Println("  dm https://example.com/file.zip")
	fmt.Println("  dm -c 16 -o myfile.iso https://example.com/largefile.iso")
}
