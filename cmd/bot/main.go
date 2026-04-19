package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/mitchellhuang/cb-cc-bot/internal/bot"
	"github.com/mitchellhuang/cb-cc-bot/internal/coinbase"
	"github.com/mitchellhuang/cb-cc-bot/internal/config"
	"github.com/mitchellhuang/cb-cc-bot/internal/gmail"
	"github.com/mitchellhuang/cb-cc-bot/internal/telegram"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	gmailClient, err := gmail.NewClient(cfg)
	if err != nil {
		log.Fatalf("gmail: %v", err)
	}

	coinbaseClient, err := coinbase.NewClient(cfg)
	if err != nil {
		log.Fatalf("coinbase: %v", err)
	}

	telegramClient := telegram.NewClient(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Println("checking API connectivity...")
	if err := gmailClient.Ping(ctx); err != nil {
		log.Fatalf("gmail ping: %v", err)
	}
	if err := coinbaseClient.Ping(ctx); err != nil {
		log.Fatalf("coinbase ping: %v", err)
	}
	if err := telegramClient.Ping(ctx); err != nil {
		log.Fatalf("telegram ping: %v", err)
	}
	log.Println("all APIs reachable, starting bot")

	if err := bot.New(gmailClient, coinbaseClient, telegramClient, cfg).Run(ctx); err != nil {
		log.Fatalf("bot: %v", err)
	}
}
