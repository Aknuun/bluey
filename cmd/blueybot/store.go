package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type Business struct {
	ID          int64
	Name        string
	OwnerChatID int64
	Code        string
	Active      bool
	CreatedAt   time.Time
}

type BotPayment struct {
	ID             string
	BusinessID     int64
	OwnerChatID    int64
	CustomerChatID int64
	Amount         int64
	Payable        int64
	Card           string
	Status         string
	OrderID        string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	PaidAt         *time.Time
	TxID           string
	TxDescription  string
}

type Report struct {
	Count int64
	Total int64
}

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS businesses (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  owner_chat_id INTEGER NOT NULL UNIQUE,
  code TEXT NOT NULL UNIQUE,
  active INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS payments (
  id TEXT PRIMARY KEY,
  business_id INTEGER NOT NULL,
  owner_chat_id INTEGER NOT NULL,
  customer_chat_id INTEGER NOT NULL DEFAULT 0,
  amount INTEGER NOT NULL,
  payable INTEGER NOT NULL,
  card TEXT NOT NULL,
  status TEXT NOT NULL,
  order_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  paid_at TEXT,
  tx_id TEXT,
  tx_description TEXT
);
CREATE INDEX IF NOT EXISTS payments_business ON payments(business_id, created_at);
CREATE INDEX IF NOT EXISTS payments_status ON payments(status);
`)
	return err
}

func (s *Store) EnsureBusiness(ctx context.Context, ownerChatID int64, name string) (*Business, error) {
	b, err := s.BusinessByOwner(ctx, ownerChatID)
	if err == nil {
		return b, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return s.AddBusiness(ctx, name, ownerChatID)
}

func (s *Store) AddBusiness(ctx context.Context, name string, ownerChatID int64) (*Business, error) {
	for i := 0; i < 5; i++ {
		code, err := businessCode()
		if err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO businesses(name, owner_chat_id, code, active, created_at) VALUES(?,?,?,1,?)`,
			name, ownerChatID, code, now.Format(time.RFC3339Nano))
		if err != nil {
			if isUnique(err) {
				continue
			}
			return nil, err
		}
		id, _ := res.LastInsertId()
		return &Business{ID: id, Name: name, OwnerChatID: ownerChatID, Code: code, Active: true, CreatedAt: now}, nil
	}
	return nil, fmt.Errorf("could not allocate a unique business code")
}

func businessCode() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "biz_" + hex.EncodeToString(b[:]), nil
}

func isUnique(err error) bool {
	return err != nil && (contains(err.Error(), "UNIQUE constraint failed") || contains(err.Error(), "constraint failed"))
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func (s *Store) BusinessByOwner(ctx context.Context, chatID int64) (*Business, error) {
	return s.scanBusiness(s.db.QueryRowContext(ctx,
		`SELECT id, name, owner_chat_id, code, active, created_at FROM businesses WHERE owner_chat_id=?`, chatID))
}

func (s *Store) BusinessByID(ctx context.Context, id int64) (*Business, error) {
	return s.scanBusiness(s.db.QueryRowContext(ctx,
		`SELECT id, name, owner_chat_id, code, active, created_at FROM businesses WHERE id=?`, id))
}

func (s *Store) BusinessByCode(ctx context.Context, code string) (*Business, error) {
	return s.scanBusiness(s.db.QueryRowContext(ctx,
		`SELECT id, name, owner_chat_id, code, active, created_at FROM businesses WHERE code=?`, code))
}

func (s *Store) scanBusiness(row *sql.Row) (*Business, error) {
	var b Business
	var active int
	var created string
	if err := row.Scan(&b.ID, &b.Name, &b.OwnerChatID, &b.Code, &active, &created); err != nil {
		return nil, err
	}
	b.Active = active != 0
	b.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return &b, nil
}

func (s *Store) ListBusinesses(ctx context.Context) ([]Business, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, owner_chat_id, code, active, created_at FROM businesses ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Business
	for rows.Next() {
		var b Business
		var active int
		var created string
		if err := rows.Scan(&b.ID, &b.Name, &b.OwnerChatID, &b.Code, &active, &created); err != nil {
			return nil, err
		}
		b.Active = active != 0
		b.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) SetBusinessActive(ctx context.Context, id int64, active bool) error {
	v := 0
	if active {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE businesses SET active=? WHERE id=?`, v, id)
	return err
}

func (s *Store) CreatePayment(ctx context.Context, p *BotPayment) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO payments
(id, business_id, owner_chat_id, customer_chat_id, amount, payable, card, status, order_id, created_at, expires_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.BusinessID, p.OwnerChatID, p.CustomerChatID, p.Amount, p.Payable, p.Card,
		p.Status, p.OrderID, p.CreatedAt.UTC().Format(time.RFC3339Nano), p.ExpiresAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) UpdatePayment(ctx context.Context, id, status string, paidAt *time.Time, txID, txDescription string) error {
	var paid any
	if paidAt != nil {
		paid = paidAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE payments SET status=?, paid_at=?, tx_id=?, tx_description=? WHERE id=?`,
		status, paid, txID, txDescription, id)
	return err
}

func (s *Store) SetCustomer(ctx context.Context, id string, chatID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE payments SET customer_chat_id=? WHERE id=?`, chatID, id)
	return err
}

func (s *Store) Payment(ctx context.Context, id string) (*BotPayment, error) {
	row := s.db.QueryRowContext(ctx, `SELECT
id, business_id, owner_chat_id, customer_chat_id, amount, payable, card, status, order_id,
created_at, expires_at, paid_at, tx_id, tx_description FROM payments WHERE id=?`, id)
	return scanPayment(row)
}

func scanPayment(row *sql.Row) (*BotPayment, error) {
	var p BotPayment
	var created, expires string
	var paid, txID, txDesc sql.NullString
	if err := row.Scan(&p.ID, &p.BusinessID, &p.OwnerChatID, &p.CustomerChatID, &p.Amount, &p.Payable,
		&p.Card, &p.Status, &p.OrderID, &created, &expires, &paid, &txID, &txDesc); err != nil {
		return nil, err
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	p.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
	if paid.Valid {
		t, _ := time.Parse(time.RFC3339Nano, paid.String)
		p.PaidAt = &t
	}
	p.TxID = txID.String
	p.TxDescription = txDesc.String
	return &p, nil
}

func (s *Store) PendingPayments(ctx context.Context) ([]BotPayment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
id, business_id, owner_chat_id, customer_chat_id, amount, payable, card, status, order_id,
created_at, expires_at, paid_at, tx_id, tx_description FROM payments WHERE status='pending' ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BotPayment
	for rows.Next() {
		var p BotPayment
		var created, expires string
		var paid, txID, txDesc sql.NullString
		if err := rows.Scan(&p.ID, &p.BusinessID, &p.OwnerChatID, &p.CustomerChatID, &p.Amount, &p.Payable,
			&p.Card, &p.Status, &p.OrderID, &created, &expires, &paid, &txID, &txDesc); err != nil {
			return nil, err
		}
		p.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		p.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
		p.TxID = txID.String
		p.TxDescription = txDesc.String
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) RecentPayments(ctx context.Context, businessID int64, limit int) ([]BotPayment, error) {
	q := `SELECT id, business_id, owner_chat_id, customer_chat_id, amount, payable, card, status, order_id,
created_at, expires_at, paid_at, tx_id, tx_description FROM payments WHERE status='paid'`
	args := []any{}
	if businessID != 0 {
		q += ` AND business_id=?`
		args = append(args, businessID)
	}
	q += ` ORDER BY paid_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BotPayment
	for rows.Next() {
		var p BotPayment
		var created, expires string
		var paid, txID, txDesc sql.NullString
		if err := rows.Scan(&p.ID, &p.BusinessID, &p.OwnerChatID, &p.CustomerChatID, &p.Amount, &p.Payable,
			&p.Card, &p.Status, &p.OrderID, &created, &expires, &paid, &txID, &txDesc); err != nil {
			return nil, err
		}
		p.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		p.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
		if paid.Valid {
			t, _ := time.Parse(time.RFC3339Nano, paid.String)
			p.PaidAt = &t
		}
		p.TxID = txID.String
		p.TxDescription = txDesc.String
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Report(ctx context.Context, businessID int64, since time.Time) (Report, error) {
	q := `SELECT COUNT(*), COALESCE(SUM(amount),0) FROM payments WHERE status='paid' AND paid_at>=?`
	args := []any{since.UTC().Format(time.RFC3339Nano)}
	if businessID != 0 {
		q += ` AND business_id=?`
		args = append(args, businessID)
	}
	var r Report
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&r.Count, &r.Total)
	return r, err
}
