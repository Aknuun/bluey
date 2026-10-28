package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

const mobileUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1"

const standaloneJS = `(() => {
  try {
    const orig = window.matchMedia.bind(window);
    window.matchMedia = (q) => {
      if (String(q).includes('display-mode: standalone')) {
        return { matches: true, media: String(q), onchange: null,
          addListener(){}, removeListener(){}, addEventListener(){}, removeEventListener(){},
          dispatchEvent(){ return false; } };
      }
      return orig(q);
    };
  } catch (e) {}
  try { Object.defineProperty(navigator, 'standalone', { get: () => true, configurable: true }); } catch (e) {}
})();`

var errLoginRequired = errors.New("login required")

// sessionState is the part of BluBank's web session that lives in sessionStorage.
// The device key pair stays in the Chrome profile's IndexedDB.
type sessionState struct {
	S5 string `json:"s5"`
	K9 string `json:"k9"`
	K3 string `json:"k3"`
}

func (b *BluBank) loadSessionState() *sessionState {
	raw, err := os.ReadFile(b.cfg.BluBankStatePath)
	if err != nil {
		return nil
	}
	var st sessionState
	if json.Unmarshal(raw, &st) != nil || (st.S5 == "" && st.K9 == "" && st.K3 == "") {
		return nil
	}
	return &st
}

func sessionScript(st *sessionState) string {
	payload, _ := json.Marshal(map[string]string{"%S5": st.S5, "%9": st.K9, "%3": st.K3})
	return fmt.Sprintf(`(() => {
  try {
    const v = %s;
    for (const k of Object.keys(v)) if (v[k]) sessionStorage.setItem(k, v[k]);
  } catch (e) {}
})();`, payload)
}

func (b *BluBank) saveSession(ctx context.Context) {
	var raw string
	js := `(() => { try { return JSON.stringify({s5:sessionStorage.getItem('%S5')||'',k9:sessionStorage.getItem('%9')||'',k3:sessionStorage.getItem('%3')||''}); } catch (e) { return ''; } })()`
	if err := runWithTimeout(ctx, 5*time.Second, chromedp.Evaluate(js, &raw)); err != nil || raw == "" {
		return
	}
	var st sessionState
	if json.Unmarshal([]byte(raw), &st) != nil || (st.S5 == "" && st.K9 == "" && st.K3 == "") {
		return
	}
	dir := filepath.Dir(b.cfg.BluBankStatePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("blubank: save session: %v", err)
		return
	}
	tmp, err := os.CreateTemp(dir, ".session-*.tmp")
	if err != nil {
		log.Printf("blubank: save session: %v", err)
		return
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return
	}
	if _, err := tmp.WriteString(raw); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	if err := os.Rename(name, b.cfg.BluBankStatePath); err != nil {
		log.Printf("blubank: save session: %v", err)
	}
}

type BluBank struct {
	cfg Config
	svc *Service

	mu       sync.Mutex
	state    string
	lastErr  string
	lastPoll time.Time

	otpCh  chan otpRequest
	pollCh chan struct{}
}

type otpRequest struct {
	code string
	resp chan error
}

type BluBankStatus struct {
	Enabled   bool       `json:"enabled"`
	State     string     `json:"state"`
	LastError string     `json:"last_error,omitempty"`
	LastPoll  *time.Time `json:"last_poll,omitempty"`
	BaseURL   string     `json:"base_url"`
}

func NewBluBank(cfg Config, svc *Service) *BluBank {
	return &BluBank{
		cfg:    cfg,
		svc:    svc,
		state:  "starting",
		otpCh:  make(chan otpRequest),
		pollCh: make(chan struct{}, 1),
	}
}

func (b *BluBank) Run(ctx context.Context) {
	for {
		err := b.run(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			b.setState("error", err.Error())
			log.Printf("blubank: %v (retrying in 15s)", err)
		}
		select {
		case <-time.After(15 * time.Second):
		case <-ctx.Done():
			return
		}
	}
}

func (b *BluBank) run(ctx context.Context) error {
	browserCtx, cancel, err := b.startBrowser(ctx)
	if err != nil {
		return err
	}
	defer cancel()

	if err := runWithTimeout(browserCtx, 30*time.Second, chromedp.Navigate(b.cfg.BluBankBaseURL+"/dashboard")); err != nil {
		return err
	}
	time.Sleep(6 * time.Second)

	if u := b.location(browserCtx); !strings.Contains(u, "/dashboard") {
		if err := b.login(browserCtx); err != nil {
			return err
		}
		if err := runWithTimeout(browserCtx, 30*time.Second, chromedp.Navigate(b.cfg.BluBankBaseURL+"/dashboard")); err != nil {
			return err
		}
		time.Sleep(5 * time.Second)
	}
	b.setState("ready", "")
	b.saveSession(browserCtx)

	txs, err := b.readTransactions(browserCtx)
	if err != nil {
		log.Printf("blubank: baseline read: %v", err)
	}
	hasSeen, err := b.svc.store.HasSeenTransactions(ctx)
	if err != nil {
		return err
	}
	if !hasSeen {
		for _, tx := range txs {
			_ = b.svc.store.MarkSeen(ctx, tx.Key, tx.AmountRial, tx.Description)
		}
		log.Printf("blubank: baselined %d transactions", len(txs))
	} else {
		b.process(ctx, txs)
	}

	ticker := time.NewTicker(b.cfg.BluBankPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case <-b.pollCh:
		}
		err := b.pollOnce(ctx, browserCtx)
		if errors.Is(err, errLoginRequired) {
			b.setState("logging_in", "")
			if e := b.login(browserCtx); e != nil {
				return e
			}
			continue
		}
		if err != nil {
			return err
		}
	}
}

func (b *BluBank) pollOnce(ctx context.Context, browserCtx context.Context) error {
	if err := runWithTimeout(browserCtx, 30*time.Second, chromedp.Navigate(b.cfg.BluBankBaseURL+"/dashboard")); err != nil {
		return err
	}
	time.Sleep(4 * time.Second)
	u := b.location(browserCtx)
	if strings.Contains(u, "device-verify") || strings.Contains(u, "/login") || strings.Contains(u, "/relogin") {
		return errLoginRequired
	}
	txs, err := b.readTransactions(browserCtx)
	if err != nil {
		return err
	}
	b.process(ctx, txs)
	b.saveSession(browserCtx)
	b.mu.Lock()
	b.lastPoll = time.Now()
	b.mu.Unlock()
	return nil
}

func (b *BluBank) process(ctx context.Context, txs []Transaction) {
	for _, tx := range txs {
		if err := b.svc.store.ProcessTransaction(ctx, tx.Key, tx.AmountRial, tx.Description, tx.Incoming); err != nil {
			log.Printf("blubank: transaction %s: %v", tx.Key, err)
		}
	}
}

func (b *BluBank) readTransactions(ctx context.Context) ([]Transaction, error) {
	var text string
	for i := 0; i < 8; i++ {
		if err := runWithTimeout(ctx, 10*time.Second, chromedp.Text("body", &text, chromedp.ByQuery)); err != nil {
			return nil, err
		}
		txs := parseTransactions(text)
		if len(txs) > 0 || strings.Contains(text, "موجودی") {
			return txs, nil
		}
		time.Sleep(time.Second)
	}
	return parseTransactions(text), nil
}

func (b *BluBank) startBrowser(ctx context.Context) (context.Context, context.CancelFunc, error) {
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts,
		chromedp.Headless,
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.UserAgent(mobileUA),
		chromedp.WindowSize(390, 844),
		chromedp.UserDataDir(b.cfg.BluBankProfileDir),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("mute-audio", true),
		chromedp.Flag("disable-background-timer-throttling", true),
		chromedp.Flag("disable-renderer-backgrounding", true),
		chromedp.Flag("disable-backgrounding-occluded-windows", true),
	)
	if b.cfg.BluBankChromePath != "" {
		opts = append(opts, chromedp.ExecPath(b.cfg.BluBankChromePath))
	}
	if b.cfg.BluBankProxy != "" {
		opts = append(opts, chromedp.ProxyServer(b.cfg.BluBankProxy))
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, opts...)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	if err := chromedp.Run(browserCtx); err != nil {
		browserCancel()
		allocCancel()
		return nil, nil, fmt.Errorf("start chrome: %w", err)
	}
	scripts := []string{standaloneJS}
	if st := b.loadSessionState(); st != nil {
		scripts = append(scripts, sessionScript(st))
		log.Printf("blubank: restoring saved web session")
	}
	if err := chromedp.Run(browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		for _, script := range scripts {
			if _, err := page.AddScriptToEvaluateOnNewDocument(script).Do(ctx); err != nil {
				return err
			}
		}
		return nil
	})); err != nil {
		browserCancel()
		allocCancel()
		return nil, nil, fmt.Errorf("inject browser scripts: %w", err)
	}
	return browserCtx, func() {
		browserCancel()
		allocCancel()
	}, nil
}

func (b *BluBank) login(ctx context.Context) error {
	b.setState("logging_in", "")
	u := b.location(ctx)
	if strings.Contains(u, "/payment/hub") || strings.Contains(u, "/dashboard") {
		b.setState("ready", "")
		b.saveSession(ctx)
		return nil
	}

	if err := runWithTimeout(ctx, 30*time.Second, chromedp.Navigate(b.cfg.BluBankBaseURL+"/login")); err != nil {
		return err
	}
	time.Sleep(3 * time.Second)
	b.clickText(ctx, "حساب بلو دارم")
	time.Sleep(time.Second)

	if err := runWithTimeout(ctx, 30*time.Second,
		chromedp.WaitVisible(`input[placeholder="نام کاربری"]`, chromedp.ByQuery)); err != nil {
		return fmt.Errorf("login form not found: %w", err)
	}
	if err := runWithTimeout(ctx, 20*time.Second,
		chromedp.Click(`input[placeholder="نام کاربری"]`, chromedp.ByQuery),
		chromedp.SendKeys(`input[placeholder="نام کاربری"]`, b.cfg.BluBankUsername, chromedp.ByQuery),
		chromedp.Click(`input[placeholder="رمز عبور"]`, chromedp.ByQuery),
		chromedp.SendKeys(`input[placeholder="رمز عبور"]`, b.cfg.BluBankPassword, chromedp.ByQuery),
	); err != nil {
		return err
	}
	time.Sleep(500 * time.Millisecond)
	if !b.clickText(ctx, "ورود به بلو") {
		return fmt.Errorf("login button not found")
	}

	u, err := b.waitURL(ctx, 25*time.Second, func(u string) bool {
		return strings.Contains(u, "device-verify") ||
			strings.Contains(u, "/dashboard") ||
			strings.Contains(u, "/payment/hub") ||
			strings.Contains(u, "/relogin")
	})
	if err != nil {
		return err
	}
	if strings.Contains(u, "device-verify") {
		return b.waitOTP(ctx)
	}
	if strings.Contains(u, "/login") || strings.Contains(u, "/relogin") {
		var text string
		_ = runWithTimeout(ctx, 5*time.Second, chromedp.Text("body", &text, chromedp.ByQuery))
		return fmt.Errorf("login failed: %s", truncate(strings.TrimSpace(text), 300))
	}
	b.setState("ready", "")
	return nil
}

func (b *BluBank) waitOTP(ctx context.Context) error {
	b.setState("otp_required", "")
	for {
		select {
		case req := <-b.otpCh:
			err := b.fillOTP(ctx, req.code)
			if err == nil {
				b.setState("ready", "")
				req.resp <- nil
				return nil
			}
			b.setState("otp_required", err.Error())
			req.resp <- err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (b *BluBank) fillOTP(ctx context.Context, code string) error {
	if err := runWithTimeout(ctx, 20*time.Second,
		chromedp.WaitVisible(`input[inputmode="numeric"]`, chromedp.ByQuery)); err != nil {
		return fmt.Errorf("OTP form not found: %w", err)
	}
	js := fmt.Sprintf(`(() => {
		const c = %s;
		const ins = [...document.querySelectorAll('input[inputmode="numeric"]')];
		const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
		ins.forEach((el, i) => {
			el.focus();
			setter.call(el, c[i] || '');
			el.dispatchEvent(new Event('input', { bubbles: true }));
			el.dispatchEvent(new Event('change', { bubbles: true }));
		});
		const btn = [...document.querySelectorAll('button[type="submit"]')].pop();
		if (btn) btn.click();
		return ins.length;
	})()`, strconv.Quote(code))
	var n int
	if err := runWithTimeout(ctx, 10*time.Second, chromedp.Evaluate(js, &n)); err != nil {
		return err
	}
	if n < 4 {
		return fmt.Errorf("expected OTP inputs, found %d", n)
	}
	time.Sleep(5 * time.Second)
	u := b.location(ctx)
	if strings.Contains(u, "/dashboard") || strings.Contains(u, "/payment/hub") {
		return nil
	}
	var text string
	_ = runWithTimeout(ctx, 5*time.Second, chromedp.Text("body", &text, chromedp.ByQuery))
	if strings.Contains(text, "نادرست") {
		return fmt.Errorf("incorrect OTP")
	}
	return fmt.Errorf("OTP not accepted (url=%s)", u)
}

func (b *BluBank) SubmitOTP(ctx context.Context, code string) error {
	b.mu.Lock()
	st := b.state
	b.mu.Unlock()
	if st != "otp_required" {
		return fmt.Errorf("OTP is not required (state=%s)", st)
	}
	req := otpRequest{code: code, resp: make(chan error, 1)}
	select {
	case b.otpCh <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return fmt.Errorf("blubank login loop is not waiting for OTP")
	}
	select {
	case err := <-req.resp:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *BluBank) PollNow() {
	select {
	case b.pollCh <- struct{}{}:
	default:
	}
}

func (b *BluBank) Status() BluBankStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := BluBankStatus{
		Enabled:   true,
		State:     b.state,
		LastError: b.lastErr,
		BaseURL:   b.cfg.BluBankBaseURL,
	}
	if !b.lastPoll.IsZero() {
		t := b.lastPoll
		st.LastPoll = &t
	}
	return st
}

func (b *BluBank) setState(state, errText string) {
	b.mu.Lock()
	b.state = state
	b.lastErr = errText
	b.mu.Unlock()
}

func (b *BluBank) location(ctx context.Context) string {
	var u string
	_ = runWithTimeout(ctx, 5*time.Second, chromedp.Location(&u))
	return u
}

func (b *BluBank) waitURL(ctx context.Context, timeout time.Duration, ok func(string) bool) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		u := b.location(ctx)
		if ok(u) {
			return u, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return b.location(ctx), fmt.Errorf("timeout waiting for navigation")
}

func (b *BluBank) clickText(ctx context.Context, text string) bool {
	js := fmt.Sprintf(`(() => {
		const t = %s;
		const els = [...document.querySelectorAll('button,[role=button],a')];
		const el = els.find(e => ((e.innerText || '').trim()).includes(t));
		if (el) { el.click(); return true; }
		return false;
	})()`, strconv.Quote(text))
	var ok bool
	_ = runWithTimeout(ctx, 5*time.Second, chromedp.Evaluate(js, &ok))
	return ok
}

func runWithTimeout(ctx context.Context, d time.Duration, actions ...chromedp.Action) error {
	c, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	return chromedp.Run(c, actions...)
}
