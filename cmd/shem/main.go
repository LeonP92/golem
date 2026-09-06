package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
	"github.com/leonp92/golem/internal/shem/worker"
)

func main() {
	configPath := flag.String("config", "shem.yaml", "path to shem.yaml configuration file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	httpClient := client.New(cfg.Orchestrator, cfg.APIKey, cfg.Name)
	exec := &worker.GolemExecutor{}
	w := worker.New(cfg, httpClient, exec)

	// Connect WebSocket (non-fatal: falls back to polling if unavailable).
	wsURL := cfg.Orchestrator
	if strings.HasPrefix(wsURL, "https://") {
		wsURL = "wss://" + wsURL[8:]
	} else if strings.HasPrefix(wsURL, "http://") {
		wsURL = "ws://" + wsURL[7:]
	}
	wsURL += "/api/ws"

	wsc := &client.WSClient{}
	w.SetWSClient(wsc)
	go func() {
		delay := time.Second
		for {
			if err := wsc.Connect(wsURL, cfg.APIKey, cfg.Name); err != nil {
				log.Printf("WS connect failed: %v, retrying in %s", err, delay)
				time.Sleep(delay)
				delay = minDuration(delay*2, 60*time.Second)
				continue
			}
			delay = time.Second // reset backoff on successful connect
			if listenErr := wsc.Listen(w.HandleMessage); listenErr != nil {
				log.Printf("WS listen ended: %v, reconnecting", listenErr)
			}
		}
	}()

	w.Start()
	log.Printf("shem %q started, connected to %s", cfg.Name, cfg.Orchestrator)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh

	log.Println("shutting down...")
	w.Shutdown()
}

// minDuration returns the smaller of two durations.
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
