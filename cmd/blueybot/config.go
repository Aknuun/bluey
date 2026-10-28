package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Token       string
	APIURL      string
	APIKey      string
	DBPath      string
	AdminChatID int64
}

func LoadConfig() (Config, error) {
	c := Config{
		Token:  strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
		APIURL: strings.TrimRight(strings.TrimSpace(os.Getenv("BLUEY_URL")), "/"),
		APIKey: strings.TrimSpace(os.Getenv("BLUEY_API_KEY")),
		DBPath: strings.TrimSpace(os.Getenv("BOT_DB_PATH")),
	}
	if c.APIKey == "" {
		c.APIKey = strings.TrimSpace(os.Getenv("API_KEY"))
	}
	if c.APIURL == "" {
		c.APIURL = "http://127.0.0.1:8080"
	}
	if c.DBPath == "" {
		c.DBPath = "/data/blueybot.db"
	}
	if c.Token == "" || c.APIKey == "" {
		return c, fmt.Errorf("TELEGRAM_BOT_TOKEN and BLUEY_API_KEY (or API_KEY) are required")
	}
	if v := strings.TrimSpace(os.Getenv("TELEGRAM_ADMIN_CHAT_ID")); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return c, fmt.Errorf("TELEGRAM_ADMIN_CHAT_ID: %w", err)
		}
		c.AdminChatID = id
	}
	return c, nil
}
