package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer store.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	svc := NewService(cfg, store)
	svc.Run(ctx)

	if cfg.BluBankEnabled {
		svc.bb = NewBluBank(cfg, svc)
		go svc.bb.Run(ctx)
	}
	if cfg.WebhookSecret == "" {
		log.Printf("warning: WEBHOOK_SECRET is empty; webhooks will not be signed")
	}

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           svc.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("receiptd listening on %s (db=%s, blubank=%v)", cfg.ListenAddr, cfg.DBPath, cfg.BluBankEnabled)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
