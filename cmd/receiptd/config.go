package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr      string
	DBPath          string
	APIKey          string
	WebhookSecret   string
	CardNumber      string
	CardHolder      string
	DefaultExpiry   time.Duration
	AllowPrivateWebhooks bool

	BluBankEnabled      bool
	BluBankBaseURL      string
	BluBankUsername     string
	BluBankPassword     string
	BluBankProfileDir   string
	BluBankChromePath   string
	BluBankProxy        string
	BluBankPollInterval time.Duration
}

func loadConfig() (Config, error) {
	c := Config{
		ListenAddr:           env("LISTEN_ADDR", ":8080"),
		DBPath:               env("DB_PATH", "./receiptd.db"),
		APIKey:               os.Getenv("API_KEY"),
		WebhookSecret:        os.Getenv("WEBHOOK_SECRET"),
		CardNumber:           os.Getenv("CARD_NUMBER"),
		CardHolder:           os.Getenv("CARD_HOLDER"),
		DefaultExpiry:        envDuration("DEFAULT_EXPIRY", 15*time.Minute),
		AllowPrivateWebhooks: envBool("ALLOW_PRIVATE_CALLBACKS", false),
		BluBankEnabled:       envBool("BLUBANK_ENABLED", false),
		BluBankBaseURL:       strings.TrimRight(env("BLUBANK_BASE_URL", "https://app.blubank.com"), "/"),
		BluBankUsername:      os.Getenv("BLUBANK_USERNAME"),
		BluBankPassword:      os.Getenv("BLUBANK_PASSWORD"),
		BluBankProfileDir:    env("BLUBANK_PROFILE_DIR", "./blubank-profile"),
		BluBankChromePath:    os.Getenv("BLUBANK_CHROME_PATH"),
		BluBankProxy:         os.Getenv("BLUBANK_PROXY"),
		BluBankPollInterval:  envDuration("BLUBANK_POLL_INTERVAL", 15*time.Second),
	}

	if c.APIKey == "" {
		return c, fmt.Errorf("API_KEY is required")
	}
	if c.CardNumber == "" {
		return c, fmt.Errorf("CARD_NUMBER is required")
	}
	if c.DefaultExpiry < time.Minute || c.DefaultExpiry > 24*time.Hour {
		return c, fmt.Errorf("DEFAULT_EXPIRY must be between 1m and 24h")
	}
	if c.BluBankEnabled {
		if c.BluBankUsername == "" || c.BluBankPassword == "" {
			return c, fmt.Errorf("BLUBANK_USERNAME and BLUBANK_PASSWORD are required when BLUBANK_ENABLED=true")
		}
		if c.BluBankPollInterval < 5*time.Second {
			return c, fmt.Errorf("BLUBANK_POLL_INTERVAL must be at least 5s")
		}
	}
	return c, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envDuration(k string, def time.Duration) time.Duration {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
