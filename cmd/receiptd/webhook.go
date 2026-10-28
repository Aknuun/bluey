package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

type TransactionInfo struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
}

type PaymentEventData struct {
	PaymentID     string           `json:"payment_id"`
	OrderID       string           `json:"order_id,omitempty"`
	Amount        int64            `json:"amount"`
	Unit          string           `json:"unit"`
	AmountRial    int64            `json:"amount_rial"`
	PayableAmount int64            `json:"payable_amount"`
	PayableRial   int64            `json:"payable_amount_rial"`
	PaidAt        *time.Time       `json:"paid_at,omitempty"`
	Transaction   *TransactionInfo `json:"transaction,omitempty"`
}

type WebhookEvent struct {
	ID        string           `json:"id"`
	Type      string           `json:"type"`
	CreatedAt time.Time        `json:"created_at"`
	Data      PaymentEventData `json:"data"`
}

func (s *Store) enqueueWebhookTx(ctx context.Context, tx *sql.Tx, p *Payment, now time.Time) error {
	if p.CallbackURL == "" {
		return nil
	}
	eventID := "evt_" + randomHex(12)
	ev := WebhookEvent{
		ID:        eventID,
		Type:      "payment.paid",
		CreatedAt: now,
		Data: PaymentEventData{
			PaymentID:     p.ID,
			OrderID:       p.OrderID,
			Amount:        p.Amount,
			Unit:          p.Unit,
			AmountRial:    p.AmountRial,
			PayableAmount: p.PayableAmount,
			PayableRial:   p.PayableRial,
			PaidAt:        p.PaidAt,
			Transaction:   &TransactionInfo{ID: p.TxID, Description: p.TxDescription},
		},
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO webhook_deliveries
		(id, payment_id, url, payload, attempts, next_attempt_at) VALUES (?, ?, ?, ?, 0, ?)`,
		eventID, p.ID, p.CallbackURL, string(payload), formatTime(now))
	return err
}

func signWebhook(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func newWebhookClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			var chosen net.IP
			for _, ip := range ips {
				if !allowPrivate && isPrivateIP(ip.IP) {
					return nil, fmt.Errorf("webhook target resolves to a private address")
				}
				if chosen == nil {
					chosen = ip.IP
				}
			}
			if chosen == nil {
				return nil, fmt.Errorf("no address for %s", host)
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(chosen.String(), port))
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
}

func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return true // carrier-grade NAT 100.64.0.0/10
	}
	return false
}

func (s *Service) webhookLoop(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.deliverDue(ctx)
		}
	}
}

func (s *Service) deliverDue(ctx context.Context) {
	due, err := s.store.DueWebhooks(ctx, time.Now(), 10)
	if err != nil {
		log.Printf("webhook: query: %v", err)
		return
	}
	for _, d := range due {
		if err := s.deliver(ctx, d); err != nil {
			attempts := d.Attempts + 1
			next := time.Now().Add(backoff(d.Attempts))
			if e := s.store.MarkWebhookFailed(ctx, d.ID, attempts, next, truncate(err.Error(), 500)); e != nil {
				log.Printf("webhook: mark failed: %v", e)
			}
			log.Printf("webhook: %s attempt %d failed: %v", d.ID, attempts, err)
			continue
		}
		if err := s.store.MarkWebhookDelivered(ctx, d.ID); err != nil {
			log.Printf("webhook: mark delivered: %v", err)
		}
	}
}

func (s *Service) deliver(ctx context.Context, d WebhookDelivery) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, strings.NewReader(d.Payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "receiptd/1")
	req.Header.Set("X-Receipt-Delivery", d.ID)
	if s.cfg.WebhookSecret != "" {
		req.Header.Set("X-Receipt-Signature", signWebhook(s.cfg.WebhookSecret, []byte(d.Payload)))
	}
	resp, err := s.webhookClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func backoff(attempts int) time.Duration {
	switch attempts {
	case 0:
		return 10 * time.Second
	case 1:
		return 30 * time.Second
	case 2:
		return time.Minute
	case 3:
		return 5 * time.Minute
	case 4:
		return 30 * time.Minute
	default:
		return 6 * time.Hour
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func (s *Service) expiryLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.store.ExpirePending(ctx, time.Now()); err != nil {
				log.Printf("expire: %v", err)
			}
			if err := s.store.PruneSeen(ctx, time.Now().Add(-30*24*time.Hour)); err != nil {
				log.Printf("prune seen: %v", err)
			}
		}
	}
}
