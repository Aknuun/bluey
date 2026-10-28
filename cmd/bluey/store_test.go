package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCreatePaymentUniqueAmounts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a, err := s.CreatePayment(ctx, CreatePaymentInput{Amount: 600000, Unit: "toman", CardNumber: "x"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreatePayment(ctx, CreatePaymentInput{Amount: 600000, Unit: "toman", CardNumber: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if a.PayableRial == b.PayableRial {
		t.Fatalf("duplicate payable amount: %d", a.PayableRial)
	}
	if a.PayableAmount <= 600000 || a.PayableAmount > 600999 {
		t.Fatalf("suffix out of range: %d", a.PayableAmount)
	}
	if a.PayableRial != a.PayableAmount*10 {
		t.Fatalf("toman conversion: %d != %d", a.PayableRial, a.PayableAmount*10)
	}
	if a.Status != "pending" {
		t.Fatalf("status = %s", a.Status)
	}
}

func TestMatchDeposit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p, err := s.CreatePayment(ctx, CreatePaymentInput{Amount: 250000, Unit: "toman", CardNumber: "x"})
	if err != nil {
		t.Fatal(err)
	}
	matched, err := s.MatchDeposit(ctx, p.PayableRial, "دریافت از کارت", "tx-1")
	if err != nil {
		t.Fatal(err)
	}
	if matched == nil || matched.ID != p.ID || matched.Status != "paid" {
		t.Fatalf("unexpected match: %+v", matched)
	}
	again, err := s.MatchDeposit(ctx, p.PayableRial, "دریافت از کارت", "tx-1")
	if err != nil {
		t.Fatal(err)
	}
	if again != nil {
		t.Fatalf("idempotency failed: %+v", again)
	}
	other, err := s.MatchDeposit(ctx, p.PayableRial, "دریافت از کارت", "tx-2")
	if err != nil {
		t.Fatal(err)
	}
	if other != nil {
		t.Fatalf("paid payment matched again: %+v", other)
	}
}

func TestExpireFreesAmount(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p, err := s.CreatePayment(ctx, CreatePaymentInput{
		Amount: 1000, Unit: "toman", CardNumber: "x", Expiry: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := s.ExpirePending(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPayment(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "expired" {
		t.Fatalf("status = %s", got.Status)
	}
	if _, err := s.CreatePayment(ctx, CreatePaymentInput{Amount: 1000, Unit: "toman", CardNumber: "x"}); err != nil {
		t.Fatal(err)
	}
}
