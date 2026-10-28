package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Bluey struct {
	base string
	key  string
	http *http.Client
}

func NewBluey(base, key string) *Bluey {
	return &Bluey{base: strings.TrimRight(base, "/"), key: key, http: &http.Client{Timeout: 20 * time.Second}}
}

type Payment struct {
	ID            string     `json:"id"`
	OrderID       string     `json:"order_id"`
	Status        string     `json:"status"`
	Amount        int64      `json:"amount"`
	Unit          string     `json:"unit"`
	PayableAmount int64      `json:"payable_amount"`
	CardNumber    string     `json:"card_number"`
	ExpiresAt     time.Time  `json:"expires_at"`
	PaidAt        *time.Time `json:"paid_at"`
	TxID          string     `json:"tx_id"`
}

func (b *Bluey) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", b.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return fmt.Errorf("bluey: %s", e.Error)
		}
		return fmt.Errorf("bluey: HTTP %d", resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (b *Bluey) CreatePayment(ctx context.Context, amount int64, orderID string, expiresIn int) (*Payment, error) {
	var p Payment
	err := b.do(ctx, http.MethodPost, "/v1/payments", map[string]any{
		"amount": amount, "unit": "toman", "order_id": orderID, "expires_in": expiresIn,
	}, &p)
	return &p, err
}

func (b *Bluey) GetPayment(ctx context.Context, id string) (*Payment, error) {
	var p Payment
	err := b.do(ctx, http.MethodGet, "/v1/payments/"+id, nil, &p)
	return &p, err
}

func (b *Bluey) CancelPayment(ctx context.Context, id string) error {
	return b.do(ctx, http.MethodPost, "/v1/payments/"+id+"/cancel", nil, nil)
}
