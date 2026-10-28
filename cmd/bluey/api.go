package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Service struct {
	cfg           Config
	store         *Store
	bb            *BluBank
	webhookClient *http.Client
}

func NewService(cfg Config, st *Store) *Service {
	return &Service{
		cfg:           cfg,
		store:         st,
		webhookClient: newWebhookClient(cfg.AllowPrivateWebhooks),
	}
}

func (s *Service) Run(ctx context.Context) {
	go s.webhookLoop(ctx)
	go s.expiryLoop(ctx)
}

func (s *Service) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/payments", s.handleCreatePayment)
	mux.HandleFunc("GET /v1/payments/{id}", s.handleGetPayment)
	mux.HandleFunc("POST /v1/payments/{id}/cancel", s.handleCancelPayment)
	mux.HandleFunc("POST /v1/payments/{id}/confirm", s.handleConfirmPayment)
	mux.HandleFunc("POST /v1/deposits", s.handleDeposit)
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/blubank/status", s.handleBluBankStatus)
	mux.HandleFunc("POST /v1/blubank/otp", s.handleBluBankOTP)
	mux.HandleFunc("POST /v1/blubank/poll", s.handleBluBankPoll)
	return s.withCORS(s.withAuth(mux))
}

func (s *Service) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Service) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			next.ServeHTTP(w, r)
			return
		}
		key := r.Header.Get("X-API-Key")
		if key == "" {
			if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
				key = strings.TrimPrefix(a, "Bearer ")
			}
		}
		if subtle.ConstantTimeCompare([]byte(key), []byte(s.cfg.APIKey)) != 1 {
			writeErr(w, http.StatusUnauthorized, "invalid API key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type createPaymentRequest struct {
	Amount      int64           `json:"amount"`
	Unit        string          `json:"unit"`
	OrderID     string          `json:"order_id"`
	CallbackURL string          `json:"callback_url"`
	Metadata    json.RawMessage `json:"metadata"`
	ExpiresIn   int             `json:"expires_in"`
}

func (s *Service) handleCreatePayment(w http.ResponseWriter, r *http.Request) {
	var req createPaymentRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Amount <= 0 {
		writeErr(w, http.StatusBadRequest, "amount must be positive")
		return
	}
	unit := strings.ToLower(req.Unit)
	if unit == "" {
		unit = "toman"
	}
	if unit != "toman" && unit != "rial" {
		writeErr(w, http.StatusBadRequest, "unit must be toman or rial")
		return
	}
	if req.CallbackURL != "" {
		if err := validateCallbackURL(req.CallbackURL); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid callback_url: "+err.Error())
			return
		}
	}
	if len(req.Metadata) > 0 && !json.Valid(req.Metadata) {
		writeErr(w, http.StatusBadRequest, "metadata must be valid JSON")
		return
	}
	expiry := s.cfg.DefaultExpiry
	if req.ExpiresIn > 0 {
		if req.ExpiresIn < 60 || req.ExpiresIn > 86400 {
			writeErr(w, http.StatusBadRequest, "expires_in must be between 60 and 86400 seconds")
			return
		}
		expiry = time.Duration(req.ExpiresIn) * time.Second
	}
	p, err := s.store.CreatePayment(r.Context(), CreatePaymentInput{
		Amount:      req.Amount,
		Unit:        unit,
		OrderID:     req.OrderID,
		CallbackURL: req.CallbackURL,
		Metadata:    req.Metadata,
		Expiry:      expiry,
		CardNumber:  s.cfg.CardNumber,
		CardHolder:  s.cfg.CardHolder,
	})
	if errors.Is(err, ErrAmountExhausted) {
		writeErr(w, http.StatusConflict, "no unique payable amount available; try again later")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Service) handleGetPayment(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetPayment(r.Context(), r.PathValue("id"))
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "payment not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Service) handleCancelPayment(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.CancelPayment(r.Context(), r.PathValue("id"))
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "payment not found")
		return
	}
	if errors.Is(err, ErrNotPending) {
		writeErr(w, http.StatusConflict, "payment is not pending")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Service) handleConfirmPayment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TxID        string `json:"tx_id"`
		Description string `json:"description"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(w, r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	p, err := s.store.MarkPaid(r.Context(), r.PathValue("id"), req.TxID, req.Description)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "payment not found")
		return
	}
	if errors.Is(err, ErrNotPending) {
		writeErr(w, http.StatusConflict, "payment is not pending")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Service) handleDeposit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AmountRial  int64  `json:"amount_rial"`
		Description string `json:"description"`
		TxID        string `json:"tx_id"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.AmountRial <= 0 || req.TxID == "" {
		writeErr(w, http.StatusBadRequest, "amount_rial and tx_id are required")
		return
	}
	p, err := s.store.MatchDeposit(r.Context(), req.AmountRial, req.Description, req.TxID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if p == nil {
		writeJSON(w, http.StatusOK, map[string]any{"matched": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"matched": true, "payment": p})
}

func (s *Service) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.store.db.PingContext(r.Context()); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "time": time.Now().UTC()})
}

func (s *Service) handleBluBankStatus(w http.ResponseWriter, r *http.Request) {
	if s.bb == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	writeJSON(w, http.StatusOK, s.bb.Status())
}

func (s *Service) handleBluBankOTP(w http.ResponseWriter, r *http.Request) {
	if s.bb == nil {
		writeErr(w, http.StatusBadRequest, "blubank adapter is disabled")
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Code = strings.TrimSpace(req.Code)
	if len(req.Code) < 4 || len(req.Code) > 8 {
		writeErr(w, http.StatusBadRequest, "invalid OTP code")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	if err := s.bb.SubmitOTP(ctx, req.Code); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.bb.Status())
}

func (s *Service) handleBluBankPoll(w http.ResponseWriter, r *http.Request) {
	if s.bb == nil {
		writeErr(w, http.StatusBadRequest, "blubank adapter is disabled")
		return
	}
	s.bb.PollNow()
	writeJSON(w, http.StatusAccepted, s.bb.Status())
}

func validateCallbackURL(raw string) error {
	if len(raw) > 2048 {
		return fmt.Errorf("too long")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https")
	}
	if u.Host == "" {
		return fmt.Errorf("missing host")
	}
	if u.User != nil {
		return fmt.Errorf("userinfo is not allowed")
	}
	return nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("empty body")
		}
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
