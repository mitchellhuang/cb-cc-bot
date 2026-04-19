package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	GmailCredentialsFile string
	GmailTokenFile       string
	CoinbaseAPIKeyName   string
	CoinbasePrivateKey   string
	TelegramBotToken     string
	TelegramChatID       string
	PollInterval         time.Duration
	MaxSellUSD           float64
	SlippageBuffer       float64
}

func Load() (*Config, error) {
	pollStr := getenv("POLL_INTERVAL", "5m")
	pollInterval, err := time.ParseDuration(pollStr)
	if err != nil {
		return nil, fmt.Errorf("invalid POLL_INTERVAL %q: %w", pollStr, err)
	}

	cfg := &Config{
		GmailCredentialsFile: getenv("GMAIL_CREDENTIALS_FILE", "credentials.json"),
		GmailTokenFile:       getenv("GMAIL_TOKEN_FILE", "token.json"),
		CoinbaseAPIKeyName:   os.Getenv("COINBASE_API_KEY_NAME"),
		CoinbasePrivateKey:   os.Getenv("COINBASE_PRIVATE_KEY"),
		TelegramBotToken:     os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:       os.Getenv("TELEGRAM_CHAT_ID"),
		PollInterval:         pollInterval,
	}

	slippageStr := getenv("SLIPPAGE_BUFFER", "0.005")
	slippageBuffer, err := strconv.ParseFloat(slippageStr, 64)
	if err != nil || slippageBuffer < 0 {
		return nil, fmt.Errorf("invalid SLIPPAGE_BUFFER %q: must be a non-negative number", slippageStr)
	}
	cfg.SlippageBuffer = slippageBuffer

	maxSellStr := os.Getenv("MAX_SELL_USD")
	if maxSellStr == "" {
		return nil, fmt.Errorf("missing required env var: MAX_SELL_USD")
	}
	maxSellUSD, err := strconv.ParseFloat(maxSellStr, 64)
	if err != nil || maxSellUSD <= 0 {
		return nil, fmt.Errorf("invalid MAX_SELL_USD %q: must be a positive number", maxSellStr)
	}
	cfg.MaxSellUSD = maxSellUSD

	required := map[string]string{
		"COINBASE_API_KEY_NAME": cfg.CoinbaseAPIKeyName,
		"COINBASE_PRIVATE_KEY":  cfg.CoinbasePrivateKey,
		"TELEGRAM_BOT_TOKEN":    cfg.TelegramBotToken,
		"TELEGRAM_CHAT_ID":      cfg.TelegramChatID,
	}
	for k, v := range required {
		if v == "" {
			return nil, fmt.Errorf("missing required env var: %s", k)
		}
	}
	return cfg, nil
}

func getenv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
