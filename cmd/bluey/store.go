package main

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound       = errors.New("not found")
	ErrNotPending     = errors.New("payment is not pending")
	ErrAmountExhausted = errors.New("no unique payable amount available")
)

type Payment struct {
	ID            string          `json:"id"`
	OrderID       string          `json:"order_id,omitempty"`
	Amount        int64           `json:"amount"`
	Unit          string          `json:"unit"`
	AmountRial    int64           `json:"amount_rial"`
	PayableAmount int64           `json:"payable_amount"`
	PayableRial   int64           `json:"payable_amount_rial"`
	Status        string          `json:"status"`
	CardNumber    string          `json:"card_number"`
	CardHolder    string          `json:"card_holder,omitempty"`
	CallbackURL   string          `json:"callback_url,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	ExpiresAt     time.Time       `json:"expires_at"`
	PaidAt        *time.Time      `json:"paid_at,omitempty"`
	TxID          string          `json:"tx_id,omitempty"`
	TxDescription string          `json:"tx_description,omitempty"`
}

type CreatePaymentInput struct {
	Amount      int64
	Unit        string
	OrderID     string
	CallbackURL string
	Metadata    json.RawMessage
	Expiry      time.Duration
	CardNumber  string
	CardHolder  string
}

type WebhookDelivery struct {
	ID        string
	PaymentID string
	URL       string
	Payload   string
	Attempts  int
}

type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS payments (
  id TEXT PRIMARY KEY,
  order_id TEXT NOT NULL DEFAULT '',
  amount INTEGER NOT NULL,
  unit TEXT NOT NULL,
  amount_rial INTEGER NOT NULL,
  payable_amount INTEGER NOT NULL,
  payable_rial INTEGER NOT NULL,
  status TEXT NOT NULL,
  card_number TEXT NOT NULL,
  card_holder TEXT NOT NULL DEFAULT '',
  callback_url TEXT NOT NULL DEFAULT '',
  metadata TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  paid_at TEXT,
  tx_id TEXT NOT NULL DEFAULT '',
  tx_description TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS payments_open_amount ON payments(payable_rial) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS payments_status_expires ON payments(status, expires_at);
CREATE TABLE IF NOT EXISTS seen_transactions (
  key TEXT PRIMARY KEY,
  payment_id TEXT NOT NULL DEFAULT '',
  amount_rial INTEGER NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  seen_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS unmatched_deposits (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  key TEXT NOT NULL UNIQUE,
  amount_rial INTEGER NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  seen_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS webhook_deliveries (
  id TEXT PRIMARY KEY,
  payment_id TEXT NOT NULL,
  url TEXT NOT NULL,
  payload TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TEXT NOT NULL,
  delivered_at TEXT,
  last_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS webhook_due ON webhook_deliveries(delivered_at, next_attempt_at);
`

func (s *Store) migrate() error {
	for _, stmt := range strings.Split(schema, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := crand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPayment(row rowScanner) (*Payment, error) {
	var p Payment
	var metadata string
	var createdAt, expiresAt string
	var paidAt sql.NullString
	err := row.Scan(
		&p.ID, &p.OrderID, &p.Amount, &p.Unit, &p.AmountRial, &p.PayableAmount, &p.PayableRial,
		&p.Status, &p.CardNumber, &p.CardHolder, &p.CallbackURL, &metadata,
		&createdAt, &expiresAt, &paidAt, &p.TxID, &p.TxDescription,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if metadata != "" {
		p.Metadata = json.RawMessage(metadata)
	}
	p.CreatedAt = parseTime(createdAt)
	p.ExpiresAt = parseTime(expiresAt)
	if paidAt.Valid {
		t := parseTime(paidAt.String)
		p.PaidAt = &t
	}
	return &p, nil
}

const paymentColumns = `id, order_id, amount, unit, amount_rial, payable_amount, payable_rial,
status, card_number, card_holder, callback_url, metadata, created_at, expires_at, paid_at, tx_id, tx_description`

func (s *Store) GetPayment(ctx context.Context, id string) (*Payment, error) {
	return scanPayment(s.db.QueryRowContext(ctx, `SELECT `+paymentColumns+` FROM payments WHERE id = ?`, id))
}

func (s *Store) CreatePayment(ctx context.Context, in CreatePaymentInput) (*Payment, error) {
	if in.Amount <= 0 || in.Amount > 1_000_000_000_000 {
		return nil, fmt.Errorf("amount out of range")
	}
	unit := strings.ToLower(in.Unit)
	if unit == "" {
		unit = "toman"
	}
	if unit != "toman" && unit != "rial" {
		return nil, fmt.Errorf("unit must be toman or rial")
	}
	mult := int64(1)
	if unit == "toman" {
		mult = 10
	}
	if in.Expiry <= 0 {
		in.Expiry = 15 * time.Minute
	}
	now := time.Now().UTC()
	if err := s.ExpirePending(ctx, now); err != nil {
		return nil, err
	}
	start := mrand.IntN(999)
	for i := 0; i < 999; i++ {
		suffix := int64((start+i)%999 + 1)
		payable := in.Amount + suffix
		payableRial := payable * mult
		id := "pay_" + randomHex(12)
		res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO payments
			(id, order_id, amount, unit, amount_rial, payable_amount, payable_rial, status,
			 card_number, card_holder, callback_url, metadata, created_at, expires_at, tx_id, tx_description)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?, ?, '', '')`,
			id, in.OrderID, in.Amount, unit, in.Amount*mult, payable, payableRial,
			in.CardNumber, in.CardHolder, in.CallbackURL, string(in.Metadata),
			formatTime(now), formatTime(now.Add(in.Expiry)))
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		if n == 1 {
			return s.GetPayment(ctx, id)
		}
	}
	return nil, ErrAmountExhausted
}

func (s *Store) ExpirePending(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE payments SET status = 'expired'
		WHERE status = 'pending' AND expires_at <= ?`, formatTime(now))
	return err
}

func (s *Store) CancelPayment(ctx context.Context, id string) (*Payment, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE payments SET status = 'canceled'
		WHERE id = ? AND status = 'pending'`, id)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		p, err := s.GetPayment(ctx, id)
		if err != nil {
			return nil, err
		}
		if p.Status != "pending" {
			return nil, ErrNotPending
		}
		return nil, ErrNotFound
	}
	return s.GetPayment(ctx, id)
}

func (s *Store) MarkPaid(ctx context.Context, id, txID, desc string) (*Payment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	p, err := scanPayment(tx.QueryRowContext(ctx, `SELECT `+paymentColumns+` FROM payments WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	if p.Status != "pending" {
		return nil, ErrNotPending
	}
	now := time.Now().UTC()
	if txID == "" {
		txID = "manual:" + id
	}
	if _, err := tx.ExecContext(ctx, `UPDATE payments SET status = 'paid', paid_at = ?, tx_id = ?, tx_description = ?
		WHERE id = ?`, formatTime(now), txID, desc, id); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO seen_transactions
		(key, payment_id, amount_rial, description, seen_at) VALUES (?, ?, ?, ?, ?)`,
		txID, id, p.PayableRial, desc, formatTime(now)); err != nil {
		return nil, err
	}
	p.Status = "paid"
	p.PaidAt = &now
	p.TxID = txID
	p.TxDescription = desc
	if err := s.enqueueWebhookTx(ctx, tx, p, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Store) MatchDeposit(ctx context.Context, amountRial int64, desc, txKey string) (*Payment, error) {
	if amountRial <= 0 || txKey == "" {
		return nil, fmt.Errorf("amount_rial and tx_id are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM seen_transactions WHERE key = ?`, txKey).Scan(&exists)
	if err == nil {
		return nil, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	now := time.Now().UTC()
	p, err := scanPayment(tx.QueryRowContext(ctx, `SELECT `+paymentColumns+`
		FROM payments WHERE payable_rial = ? AND status = 'pending' ORDER BY created_at LIMIT 1`, amountRial))
	if errors.Is(err, ErrNotFound) {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO seen_transactions
			(key, payment_id, amount_rial, description, seen_at) VALUES (?, '', ?, ?, ?)`,
			txKey, amountRial, desc, formatTime(now)); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO unmatched_deposits
			(key, amount_rial, description, seen_at) VALUES (?, ?, ?, ?)`,
			txKey, amountRial, desc, formatTime(now)); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `UPDATE payments SET status = 'paid', paid_at = ?, tx_id = ?, tx_description = ?
		WHERE id = ?`, formatTime(now), txKey, desc, p.ID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO seen_transactions
		(key, payment_id, amount_rial, description, seen_at) VALUES (?, ?, ?, ?, ?)`,
		txKey, p.ID, amountRial, desc, formatTime(now)); err != nil {
		return nil, err
	}
	p.Status = "paid"
	p.PaidAt = &now
	p.TxID = txKey
	p.TxDescription = desc
	if err := s.enqueueWebhookTx(ctx, tx, p, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Store) MarkSeen(ctx context.Context, txKey string, amountRial int64, desc string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO seen_transactions
		(key, payment_id, amount_rial, description, seen_at) VALUES (?, '', ?, ?, ?)`,
		txKey, amountRial, desc, formatTime(time.Now()))
	return err
}

func (s *Store) ProcessTransaction(ctx context.Context, txKey string, amountRial int64, desc string, incoming bool) error {
	if incoming {
		_, err := s.MatchDeposit(ctx, amountRial, desc, txKey)
		return err
	}
	return s.MarkSeen(ctx, txKey, amountRial, desc)
}

func (s *Store) HasSeenTransactions(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM seen_transactions)`).Scan(&n)
	return n == 1, err
}

func (s *Store) PruneSeen(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM seen_transactions WHERE seen_at < ?`, formatTime(before))
	return err
}

func (s *Store) DueWebhooks(ctx context.Context, now time.Time, limit int) ([]WebhookDelivery, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, payment_id, url, payload, attempts
		FROM webhook_deliveries
		WHERE delivered_at IS NULL AND attempts < 12 AND next_attempt_at <= ?
		ORDER BY next_attempt_at LIMIT ?`, formatTime(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebhookDelivery
	for rows.Next() {
		var d WebhookDelivery
		if err := rows.Scan(&d.ID, &d.PaymentID, &d.URL, &d.Payload, &d.Attempts); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) MarkWebhookDelivered(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE webhook_deliveries SET delivered_at = ?, last_error = ''
		WHERE id = ?`, formatTime(time.Now()), id)
	return err
}

func (s *Store) MarkWebhookFailed(ctx context.Context, id string, attempts int, next time.Time, errText string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE webhook_deliveries
		SET attempts = ?, next_attempt_at = ?, last_error = ? WHERE id = ?`,
		attempts, formatTime(next), errText, id)
	return err
}
