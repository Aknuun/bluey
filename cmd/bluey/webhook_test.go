package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWebhookDeliveryAndSignature(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	var body []byte
	var signature string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		signature = r.Header.Get("X-Receipt-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	svc := NewService(Config{WebhookSecret: "secret", AllowPrivateWebhooks: true}, st)
	p, err := st.CreatePayment(ctx, CreatePaymentInput{
		Amount: 10000, Unit: "toman", CardNumber: "x", CallbackURL: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.MatchDeposit(ctx, p.PayableRial, "دریافت از کارت", "tx-hook-1"); err != nil {
		t.Fatal(err)
	}
	svc.deliverDue(ctx)

	if len(body) == 0 {
		t.Fatal("webhook was not delivered")
	}
	if signature != signWebhook("secret", body) {
		t.Fatalf("bad signature: %q", signature)
	}
	var ev WebhookEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "payment.paid" || ev.Data.PaymentID != p.ID {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

func TestWebhookBlocksPrivateTargets(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	svc := NewService(Config{AllowPrivateWebhooks: false}, st)

	p, err := st.CreatePayment(ctx, CreatePaymentInput{
		Amount: 10000, Unit: "toman", CardNumber: "x", CallbackURL: "http://127.0.0.1:9/hook",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.MatchDeposit(ctx, p.PayableRial, "دریافت از کارت", "tx-hook-2"); err != nil {
		t.Fatal(err)
	}
	svc.deliverDue(ctx)

	due, err := st.DueWebhooks(ctx, time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Attempts != 1 {
		t.Fatalf("expected one failed attempt, got %+v", due)
	}
}
