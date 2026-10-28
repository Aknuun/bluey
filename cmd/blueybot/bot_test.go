package main

import (
	"strings"
	"testing"
)

func TestParseAmount(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"10000", 10000, true},
		{"10,000", 10000, true},
		{"۱۰٬۰۰۰", 10000, true},
		{"۱۰۰۰۰ تومان", 10000, true},
		{"999", 0, false},
		{"abc", 0, false},
		{"500000001", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, err := parseAmount(c.in)
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("parseAmount(%q) = %d, %v; want %d", c.in, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("parseAmount(%q) should fail", c.in)
		}
	}
}

func TestParseCommand(t *testing.T) {
	if c, a := parseCommand("/new 10,000"); c != "/new" || a != "10,000" {
		t.Fatalf("got %q %q", c, a)
	}
	if c, a := parseCommand("/start@bluey_bot"); c != "/start" || a != "" {
		t.Fatalf("got %q %q", c, a)
	}
	if c, a := parseCommand("hello"); c != "" || a != "hello" {
		t.Fatalf("got %q %q", c, a)
	}
}

func TestFormatToman(t *testing.T) {
	if got := formatToman(10059); got != "10,059" {
		t.Fatalf("got %q", got)
	}
	if got := formatToman(1000); got != "1,000" {
		t.Fatalf("got %q", got)
	}
}

func TestReceiptBody(t *testing.T) {
	p := &BotPayment{
		ID:      "pay_1",
		Payable: 10059,
		Status:  "paid",
		Card:    "6037-1111",
		TxID:    "tx-1",
	}
	body := receiptBody(p, "<Shop>")
	for _, want := range []string{"10,059", "تایید شده", "6037-1111", "tx-1", "&lt;Shop&gt;"} {
		if !strings.Contains(body, want) {
			t.Fatalf("receipt missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "<Shop>") {
		t.Fatalf("business name not escaped")
	}
}
