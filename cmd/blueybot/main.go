package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatal(err)
	}
	st, err := OpenStore(cfg.DBPath)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	tg := NewTelegram(cfg.Token)
	api := NewBluey(cfg.APIURL, cfg.APIKey)
	bot := NewBot(cfg, tg, st, api)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := bot.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
