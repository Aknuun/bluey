package main

import (
	"strconv"
	"testing"
)

const dashboardFixture = `۱۱۹٬۵۹۹٬۸۸۳ ریال
موجودی

دریافت از سپرده
سه‌شنبه، ۵ آبان ۱۴۰۵ ۲۲:۴۸

۶٬۲۰۰٬۰۰۰ ریال

انتقال به کارت
سه‌شنبه، ۵ آبان ۱۴۰۵ ۲۲:۴۰

۱٬۰۰۰٬۰۰۰ ریال

خرید اینترنتی
سه‌شنبه، ۵ آبان ۱۴۰۵ ۲۱:۱۰

۲۵۰٬۰۰۰ ریال

شارژ حساب
سه‌شنبه، ۵ آبان ۱۴۰۵ ۲۲:۵۰

۱۰۷٬۱۷۰ ریال`

func TestParseTransactions(t *testing.T) {
	txs := parseTransactions(dashboardFixture)
	if len(txs) != 4 {
		t.Fatalf("got %d transactions, want 4: %+v", len(txs), txs)
	}
	if !txs[0].Incoming || txs[0].AmountRial != 6200000 || txs[0].Description != "دریافت از سپرده" {
		t.Fatalf("first tx: %+v", txs[0])
	}
	if txs[1].Incoming || txs[1].AmountRial != 1000000 {
		t.Fatalf("second tx: %+v", txs[1])
	}
	if txs[2].Incoming || txs[2].AmountRial != 250000 {
		t.Fatalf("third tx: %+v", txs[2])
	}
	if !txs[3].Incoming || txs[3].AmountRial != 107170 || txs[3].Description != "شارژ حساب" {
		t.Fatalf("fourth tx: %+v", txs[3])
	}
	for _, tx := range txs {
		if tx.Key == "" {
			t.Fatal("empty key")
		}
	}
}

func TestParseDuplicateKeys(t *testing.T) {
	text := `دریافت از کارت
دوشنبه، ۴ آبان ۱۴۰۵ ۱۰:۰۰

۱٬۰۰۰ ریال

دریافت از کارت
دوشنبه، ۴ آبان ۱۴۰۵ ۱۰:۰۰

۱٬۰۰۰ ریال`
	txs := parseTransactions(text)
	if len(txs) != 2 {
		t.Fatalf("got %d", len(txs))
	}
	if txs[0].Key == txs[1].Key {
		t.Fatal("duplicate keys")
	}
}

func TestParseAmount(t *testing.T) {
	cases := map[string]int64{
		"۶٬۲۰۰٬۰۰۰": 6200000,
		"1200":      1200,
		"۱,۲۳۴":     1234,
	}
	for in, want := range cases {
		if got := parseAmount(in); got != want {
			t.Errorf("parseAmount(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseIncomingPayment(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	p, err := s.CreatePayment(ctx, CreatePaymentInput{Amount: 600000, Unit: "toman", CardNumber: "x"})
	if err != nil {
		t.Fatal(err)
	}
	text := "دریافت از کارت\nسه‌شنبه، ۵ آبان ۱۴۰۵ ۲۲:۴۸\n\n" + persianDigits(p.PayableRial) + " ریال"
	txs := parseTransactions(text)
	if len(txs) != 1 || !txs[0].Incoming || txs[0].AmountRial != p.PayableRial {
		t.Fatalf("parse: %+v want %d", txs, p.PayableRial)
	}
	if err := s.ProcessTransaction(ctx, txs[0].Key, txs[0].AmountRial, txs[0].Description, txs[0].Incoming); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPayment(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "paid" {
		t.Fatalf("status = %s", got.Status)
	}
}

func persianDigits(n int64) string {
	s := []rune(strconv.FormatInt(n, 10))
	for i, r := range s {
		s[i] = '۰' + (r - '0')
	}
	return string(s)
}
