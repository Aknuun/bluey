package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"html"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

var tehran = time.FixedZone("IRST", 3*3600+30*60)

type Bot struct {
	cfg      Config
	tg       *Telegram
	store    *Store
	api      *Bluey
	username string
	offset   int64

	mu       sync.Mutex
	watching map[string]bool
	sessions map[int64]int64
}

func NewBot(cfg Config, tg *Telegram, st *Store, api *Bluey) *Bot {
	return &Bot{
		cfg: cfg, tg: tg, store: st, api: api,
		watching: make(map[string]bool),
		sessions: make(map[int64]int64),
	}
}

func (b *Bot) Run(ctx context.Context) error {
	me, err := b.tg.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("telegram getMe: %w", err)
	}
	b.username = me.Username
	log.Printf("blueybot @%s started (admin=%d)", b.username, b.cfg.AdminChatID)

	if pending, err := b.store.PendingPayments(ctx); err == nil {
		for _, p := range pending {
			b.startWatch(ctx, p.ID)
		}
	}

	for {
		updates, err := b.tg.GetUpdates(ctx, b.offset)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("getUpdates: %v", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(3 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			b.offset = u.UpdateID + 1
			b.handle(ctx, u)
		}
	}
}

func (b *Bot) handle(ctx context.Context, u Update) {
	switch {
	case u.Message != nil:
		b.handleMessage(ctx, u.Message)
	case u.CallbackQuery != nil:
		b.handleCallback(ctx, u.CallbackQuery)
	}
}

func (b *Bot) handleMessage(ctx context.Context, m *Message) {
	if m.Text == "" {
		return
	}
	cmd, arg := parseCommand(m.Text)
	switch cmd {
	case "/start":
		if strings.HasPrefix(arg, "biz_") {
			b.payWithCode(ctx, m.Chat.ID, arg)
			return
		}
		b.welcome(ctx, m.Chat.ID)
	case "/help":
		b.help(ctx, m.Chat.ID)
	case "/business", "/register":
		b.registerBusiness(ctx, m.Chat.ID, arg)
	case "/new", "/paynew":
		b.newPayment(ctx, m.Chat.ID, arg)
	case "/pay":
		b.payWithCode(ctx, m.Chat.ID, arg)
	case "/status":
		b.showStatus(ctx, m.Chat.ID, arg)
	case "/cancel":
		b.cancelPayment(ctx, m.Chat.ID, arg)
	case "/report", "/receipts":
		b.report(ctx, m.Chat.ID)
	case "/link":
		b.paymentLink(ctx, m.Chat.ID)
	case "/settings":
		b.settings(ctx, m.Chat.ID)
	case "/admin":
		b.admin(ctx, m.Chat.ID)
	default:
		if cmd == "" {
			b.help(ctx, m.Chat.ID)
			return
		}
		b.send(ctx, m.Chat.ID, "دستور ناشناخته. /help را بزن.", nil)
	}
}

func (b *Bot) welcome(ctx context.Context, chatID int64) {
	biz, _ := b.store.BusinessByOwner(ctx, chatID)
	title := "👋 <b>Bluey</b>\nتایید خودکار رسید کارت‌به‌کارت."
	if biz != nil {
		title += "\n\n🏪 کسب‌وکار: <b>" + html.EscapeString(biz.Name) + "</b>"
	}
	b.send(ctx, chatID, title, b.mainMenu())
}

func (b *Bot) help(ctx context.Context, chatID int64) {
	text := `📚 <b>راهنما</b>

/start — منوی اصلی
/business نام — ساخت یا مشاهده کسب‌وکار
/new 10000 — ساخت پرداخت ۱۰٬۰۰۰ تومانی
/status — وضعیت آخرین پرداخت
/cancel — لغو آخرین پرداخت باز
/report — گزارش ۳۰ روز + CSV
/link — لینک پرداخت کسب‌وکار
/pay biz_xxxx — پرداخت به کسب‌وکار با کد
/settings — تنظیمات کسب‌وکار
/admin — مدیریت (فقط ادمین)

مبلغ را دقیقاً همان عددی که ربات می‌گوید واریز کن.`
	b.send(ctx, chatID, text, b.mainMenu())
}

func (b *Bot) registerBusiness(ctx context.Context, chatID int64, name string) {
	if biz, _ := b.store.BusinessByOwner(ctx, chatID); biz != nil {
		b.send(ctx, chatID, "🏪 کسب‌وکار شما قبلاً ساخته شده:\n<b>"+html.EscapeString(biz.Name)+"</b>\nکد: <code>"+biz.Code+"</code>", b.businessMenu())
		return
	}
	if strings.TrimSpace(name) == "" {
		b.send(ctx, chatID, "نام کسب‌وکار را بفرست:\n<code>/business فروشگاه من</code>", nil)
		return
	}
	biz, err := b.store.EnsureBusiness(ctx, chatID, strings.TrimSpace(name))
	if err != nil {
		b.send(ctx, chatID, "ساخت کسب‌وکار ناموفق بود.", nil)
		return
	}
	b.send(ctx, chatID, "✅ کسب‌وکار ساخته شد.\n🏪 <b>"+html.EscapeString(biz.Name)+"</b>\nکد: <code>"+biz.Code+"</code>", b.businessMenu())
}

func (b *Bot) newPayment(ctx context.Context, chatID int64, arg string) {
	biz, err := b.businessFor(ctx, chatID)
	if err != nil {
		b.send(ctx, chatID, "اول یک کسب‌وکار بساز (<code>/business نام</code>) یا با کد وارد شو (<code>/pay biz_xxxx</code>).", nil)
		return
	}
	if !biz.Active {
		b.send(ctx, chatID, "این کسب‌وکار غیرفعال است.", nil)
		return
	}
	amount, err := parseAmount(arg)
	if err != nil {
		b.send(ctx, chatID, "مبلغ نامعتبر است. مثال:\n<code>/new 10000</code>", nil)
		return
	}
	orderID := fmt.Sprintf("tg-%d-%d", chatID, time.Now().UnixNano())
	p, err := b.api.CreatePayment(ctx, amount, orderID, 900)
	if err != nil {
		b.send(ctx, chatID, "ساخت پرداخت ناموفق بود: "+html.EscapeString(err.Error()), nil)
		return
	}
	bp := &BotPayment{
		ID: p.ID, BusinessID: biz.ID, OwnerChatID: biz.OwnerChatID,
		CustomerChatID: chatID, Amount: amount, Payable: p.PayableAmount,
		Card: p.CardNumber, Status: p.Status, OrderID: orderID,
		CreatedAt: time.Now().UTC(), ExpiresAt: p.ExpiresAt,
	}
	if err := b.store.CreatePayment(ctx, bp); err != nil {
		b.send(ctx, chatID, "ذخیره پرداخت ناموفق بود.", nil)
		return
	}
	b.startWatch(ctx, p.ID)
	b.sendPayment(ctx, chatID, bp)
}

func (b *Bot) sendPayment(ctx context.Context, chatID int64, p *BotPayment) {
	text := fmt.Sprintf(`🟢 <b>پرداخت جدید</b>

🏪 %s
💰 مبلغ دقیق: <b>%s تومان</b>
💳 کارت: <code>%s</code>
🆔 سفارش: <code>%s</code>
⏳ اعتبار تا: %s

⚠️ دقیقاً همین مبلغ را واریز کن؛ کمتر یا بیشتر تطبیق نمی‌خورد.`,
		html.EscapeString(b.businessName(ctx, p.BusinessID)),
		formatToman(p.Payable), html.EscapeString(p.Card), html.EscapeString(p.ID),
		p.ExpiresAt.In(tehran).Format("15:04"))
	markup := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{
		{Text: "🔄 بروزرسانی", CallbackData: "pay:" + p.ID},
		{Text: "🧾 رسید", CallbackData: "receipt:" + p.ID},
	}, {
		{Text: "❌ لغو", CallbackData: "cancel:" + p.ID},
	}}}
	b.send(ctx, chatID, text, markup)
}

func (b *Bot) showStatus(ctx context.Context, chatID int64, id string) {
	p, err := b.latestPayment(ctx, chatID, id)
	if err != nil {
		b.send(ctx, chatID, "پرداختی پیدا نشد.", nil)
		return
	}
	b.sendPayment(ctx, chatID, p)
}

func (b *Bot) cancelPayment(ctx context.Context, chatID int64, id string) {
	p, err := b.latestPayment(ctx, chatID, id)
	if err != nil {
		b.send(ctx, chatID, "پرداختی پیدا نشد.", nil)
		return
	}
	if p.Status != "pending" {
		b.send(ctx, chatID, "این پرداخت دیگر باز نیست.", nil)
		return
	}
	if chatID != p.OwnerChatID && chatID != p.CustomerChatID && !b.isAdmin(chatID) {
		b.send(ctx, chatID, "اجازه لغو این پرداخت را نداری.", nil)
		return
	}
	if err := b.api.CancelPayment(ctx, p.ID); err != nil {
		b.send(ctx, chatID, "لغو ناموفق بود.", nil)
		return
	}
	now := time.Now().UTC()
	b.store.UpdatePayment(ctx, p.ID, "canceled", &now, "", "")
	b.send(ctx, chatID, "❌ پرداخت لغو شد.", nil)
}

func (b *Bot) payWithCode(ctx context.Context, chatID int64, code string) {
	code = strings.TrimSpace(code)
	biz, err := b.store.BusinessByCode(ctx, code)
	if err != nil || !biz.Active {
		b.send(ctx, chatID, "کد کسب‌وکار معتبر نیست.", nil)
		return
	}
	b.mu.Lock()
	b.sessions[chatID] = biz.ID
	b.mu.Unlock()
	b.send(ctx, chatID, fmt.Sprintf("🏪 <b>%s</b>\nبرای پرداخت مبلغ را بفرست:\n<code>/new 10000</code>", html.EscapeString(biz.Name)), nil)
}

func (b *Bot) report(ctx context.Context, chatID int64) {
	biz, err := b.store.BusinessByOwner(ctx, chatID)
	if err != nil {
		b.send(ctx, chatID, "اول یک کسب‌وکار بساز: <code>/business نام</code>", nil)
		return
	}
	since := time.Now().AddDate(0, 0, -30)
	r, _ := b.store.Report(ctx, biz.ID, since)
	recent, _ := b.store.RecentPayments(ctx, biz.ID, 5)
	var sb strings.Builder
	fmt.Fprintf(&sb, "📊 <b>گزارش ۳۰ روز</b>\n\nتعداد: <b>%d</b>\nجمع: <b>%s تومان</b>\n\n", r.Count, formatToman(r.Total))
	for _, p := range recent {
		fmt.Fprintf(&sb, "• %s — %s تومان — %s\n", p.CreatedAt.In(tehran).Format("01/02 15:04"), formatToman(p.Payable), statusFa(p.Status))
	}
	markup := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{
		{Text: "📥 CSV", CallbackData: "rep:csv:" + strconv.FormatInt(biz.ID, 10)},
		{Text: "🔄 بروزرسانی", CallbackData: "m:report"},
	}}}
	b.send(ctx, chatID, sb.String(), markup)
}

func (b *Bot) paymentLink(ctx context.Context, chatID int64) {
	biz, err := b.store.BusinessByOwner(ctx, chatID)
	if err != nil {
		b.send(ctx, chatID, "اول یک کسب‌وکار بساز: <code>/business نام</code>", nil)
		return
	}
	if b.username == "" {
		b.send(ctx, chatID, "نام کاربری ربات در دسترس نیست.", nil)
		return
	}
	link := fmt.Sprintf("https://t.me/%s?start=%s", b.username, biz.Code)
	b.send(ctx, chatID, "🔗 <b>لینک پرداخت</b>\n<code>"+link+"</code>\n\nمشتری با این لینک وارد می‌شود و مبلغ را می‌فرستد.", &InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{{{Text: "🚀 باز کردن", URL: link}}},
	})
}

func (b *Bot) settings(ctx context.Context, chatID int64) {
	biz, err := b.store.BusinessByOwner(ctx, chatID)
	if err != nil {
		b.send(ctx, chatID, "اول یک کسب‌وکار بساز: <code>/business نام</code>", nil)
		return
	}
	b.send(ctx, chatID, settingsText(biz), b.businessMenu())
}

func (b *Bot) admin(ctx context.Context, chatID int64) {
	if !b.isAdmin(chatID) {
		b.send(ctx, chatID, "دسترسی نداری.", nil)
		return
	}
	bis, err := b.store.ListBusinesses(ctx)
	if err != nil {
		b.send(ctx, chatID, "خطا در خواندن کسب‌وکارها.", nil)
		return
	}
	var sb strings.Builder
	sb.WriteString("🛠 <b>مدیریت</b>\n\n")
	for _, biz := range bis {
		state := "فعال"
		if !biz.Active {
			state = "غیرفعال"
		}
		fmt.Fprintf(&sb, "#%d %s — مالک %d — %s — <code>%s</code>\n", biz.ID, html.EscapeString(biz.Name), biz.OwnerChatID, state, biz.Code)
	}
	if len(bis) == 0 {
		sb.WriteString("کسب‌وکاری ثبت نشده.")
	}
	b.send(ctx, chatID, sb.String(), nil)
}

func (b *Bot) handleCallback(ctx context.Context, q *CallbackQuery) {
	b.tg.AnswerCallback(ctx, q.ID, "")
	chatID := q.From.ID
	data := q.Data
	switch {
	case data == "m:new":
		b.send(ctx, chatID, "مبلغ را بفرست:\n<code>/new 10000</code>", nil)
	case data == "m:report":
		b.report(ctx, chatID)
	case data == "m:link":
		b.paymentLink(ctx, chatID)
	case data == "m:biz" || data == "m:settings":
		b.settings(ctx, chatID)
	case data == "m:help":
		b.help(ctx, chatID)
	case data == "m:admin":
		b.admin(ctx, chatID)
	case data == "m:pay":
		b.send(ctx, chatID, "کد کسب‌وکار را بفرست:\n<code>/pay biz_xxxx</code>", nil)
	case strings.HasPrefix(data, "pay:"):
		b.showStatus(ctx, chatID, strings.TrimPrefix(data, "pay:"))
	case strings.HasPrefix(data, "receipt:"):
		b.sendReceipt(ctx, chatID, strings.TrimPrefix(data, "receipt:"))
	case strings.HasPrefix(data, "cancel:"):
		b.cancelPayment(ctx, chatID, strings.TrimPrefix(data, "cancel:"))
	case strings.HasPrefix(data, "rep:csv:"):
		b.sendCSV(ctx, chatID, strings.TrimPrefix(data, "rep:csv:"))
	case strings.HasPrefix(data, "biz:on:"):
		b.toggleBusiness(ctx, chatID, strings.TrimPrefix(data, "biz:on:"), true)
	case strings.HasPrefix(data, "biz:off:"):
		b.toggleBusiness(ctx, chatID, strings.TrimPrefix(data, "biz:off:"), false)
	}
}

func (b *Bot) toggleBusiness(ctx context.Context, chatID int64, rawID string, active bool) {
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		return
	}
	biz, err := b.store.BusinessByID(ctx, id)
	if err != nil || (biz.OwnerChatID != chatID && !b.isAdmin(chatID)) {
		b.send(ctx, chatID, "اجازه تغییر این کسب‌وکار را نداری.", nil)
		return
	}
	if err := b.store.SetBusinessActive(ctx, id, active); err != nil {
		b.send(ctx, chatID, "تغییر وضعیت ناموفق بود.", nil)
		return
	}
	biz.Active = active
	b.send(ctx, chatID, settingsText(biz), b.businessMenu())
}

func (b *Bot) sendReceipt(ctx context.Context, chatID int64, id string) {
	p, err := b.store.Payment(ctx, id)
	if err != nil {
		b.send(ctx, chatID, "رسید پیدا نشد.", nil)
		return
	}
	if chatID != p.OwnerChatID && chatID != p.CustomerChatID && !b.isAdmin(chatID) {
		b.send(ctx, chatID, "اجازه دیدن این رسید را نداری.", nil)
		return
	}
	b.send(ctx, chatID, receiptText(p, b.businessName(ctx, p.BusinessID)), nil)
}

func (b *Bot) sendCSV(ctx context.Context, chatID int64, rawID string) {
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		return
	}
	biz, err := b.store.BusinessByID(ctx, id)
	if err != nil || (biz.OwnerChatID != chatID && !b.isAdmin(chatID)) {
		b.send(ctx, chatID, "اجازه دریافت گزارش را نداری.", nil)
		return
	}
	ps, err := b.store.RecentPayments(ctx, id, 500)
	if err != nil {
		b.send(ctx, chatID, "خطا در ساخت گزارش.", nil)
		return
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.Write([]string{"id", "amount_toman", "payable_toman", "status", "order_id", "created_at", "paid_at", "tx_id"})
	for _, p := range ps {
		paid := ""
		if p.PaidAt != nil {
			paid = p.PaidAt.In(tehran).Format(time.RFC3339)
		}
		w.Write([]string{p.ID, strconv.FormatInt(p.Amount, 10), strconv.FormatInt(p.Payable, 10), p.Status, p.OrderID, p.CreatedAt.In(tehran).Format(time.RFC3339), paid, p.TxID})
	}
	w.Flush()
	if err := b.tg.SendDocument(ctx, chatID, "bluey-report.csv", buf.Bytes(), "گزارش پرداخت‌های "+biz.Name); err != nil {
		b.send(ctx, chatID, "ارسال فایل ناموفق بود.", nil)
	}
}

func (b *Bot) startWatch(ctx context.Context, id string) {
	b.mu.Lock()
	if b.watching[id] {
		b.mu.Unlock()
		return
	}
	b.watching[id] = true
	b.mu.Unlock()
	go b.watch(ctx, id)
}

func (b *Bot) watch(ctx context.Context, id string) {
	defer func() {
		b.mu.Lock()
		delete(b.watching, id)
		b.mu.Unlock()
	}()
	p, err := b.store.Payment(ctx, id)
	if err != nil {
		return
	}
	deadline := p.ExpiresAt.Add(10 * time.Minute)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if time.Now().After(deadline) {
			return
		}
		live, err := b.api.GetPayment(ctx, id)
		if err != nil {
			continue
		}
		switch live.Status {
		case "paid":
			now := time.Now().UTC()
			b.store.UpdatePayment(ctx, id, "paid", &now, live.TxID, "")
			b.notifyPaid(ctx, id)
			return
		case "expired", "canceled":
			b.store.UpdatePayment(ctx, id, live.Status, nil, "", "")
			b.notifyStatus(ctx, id, live.Status)
			return
		}
	}
}

func (b *Bot) notifyPaid(ctx context.Context, id string) {
	p, err := b.store.Payment(ctx, id)
	if err != nil {
		return
	}
	name := b.businessName(ctx, p.BusinessID)
	text := "✅ <b>پرداخت تایید شد</b>\n\n" + receiptBody(p, name)
	b.send(ctx, p.CustomerChatID, text, nil)
	if p.OwnerChatID != p.CustomerChatID {
		b.send(ctx, p.OwnerChatID, "💰 <b>واریز جدید</b>\n\n"+receiptBody(p, name), nil)
	}
}

func (b *Bot) notifyStatus(ctx context.Context, id, status string) {
	p, err := b.store.Payment(ctx, id)
	if err != nil {
		return
	}
	b.send(ctx, p.CustomerChatID, "⚠️ وضعیت پرداخت: <b>"+statusFa(status)+"</b>\nسفارش: <code>"+p.ID+"</code>", nil)
}

func (b *Bot) latestPayment(ctx context.Context, chatID int64, id string) (*BotPayment, error) {
	if id != "" {
		p, err := b.store.Payment(ctx, id)
		if err != nil {
			return nil, err
		}
		if chatID != p.OwnerChatID && chatID != p.CustomerChatID && !b.isAdmin(chatID) {
			return nil, errors.New("forbidden")
		}
		return p, nil
	}
	biz, err := b.store.BusinessByOwner(ctx, chatID)
	if err == nil {
		ps, _ := b.store.RecentPayments(ctx, biz.ID, 1)
		if len(ps) > 0 {
			return &ps[0], nil
		}
	}
	ps, _ := b.store.RecentPayments(ctx, 0, 1)
	if len(ps) > 0 {
		return &ps[0], nil
	}
	return nil, errors.New("not found")
}

func (b *Bot) businessFor(ctx context.Context, chatID int64) (*Business, error) {
	b.mu.Lock()
	id := b.sessions[chatID]
	b.mu.Unlock()
	if id != 0 {
		if biz, err := b.store.BusinessByID(ctx, id); err == nil {
			return biz, nil
		}
	}
	return b.store.BusinessByOwner(ctx, chatID)
}

func (b *Bot) businessName(ctx context.Context, id int64) string {
	if biz, err := b.store.BusinessByID(ctx, id); err == nil {
		return biz.Name
	}
	return "کسب‌وکار"
}

func (b *Bot) isAdmin(chatID int64) bool {
	return b.cfg.AdminChatID != 0 && chatID == b.cfg.AdminChatID
}

func (b *Bot) send(ctx context.Context, chatID int64, text string, markup *InlineKeyboardMarkup) {
	if err := b.tg.SendMessage(ctx, chatID, text, markup); err != nil {
		log.Printf("sendMessage(%d): %v", chatID, err)
	}
}

func (b *Bot) mainMenu() *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{{Text: "🟢 پرداخت جدید", CallbackData: "m:new"}, {Text: "🔵 گزارش", CallbackData: "m:report"}},
		{{Text: "🟣 لینک پرداخت", CallbackData: "m:link"}, {Text: "🟠 کسب‌وکار من", CallbackData: "m:biz"}},
		{{Text: "🟡 تنظیمات", CallbackData: "m:settings"}, {Text: "⚪️ راهنما", CallbackData: "m:help"}},
	}}
}

func (b *Bot) businessMenu() *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{{Text: "🟢 پرداخت جدید", CallbackData: "m:new"}, {Text: "🔵 گزارش", CallbackData: "m:report"}},
		{{Text: "🟣 لینک پرداخت", CallbackData: "m:link"}, {Text: "⚙️ تنظیمات", CallbackData: "m:settings"}},
	}}
}

func settingsText(biz *Business) string {
	state := "فعال 🟢"
	if !biz.Active {
		state = "غیرفعال 🔴"
	}
	return fmt.Sprintf("🏪 <b>%s</b>\nکد: <code>%s</code>\nوضعیت: %s\nمالک: <code>%d</code>", html.EscapeString(biz.Name), biz.Code, state, biz.OwnerChatID)
}

func receiptText(p *BotPayment, name string) string {
	return "🧾 <b>رسید پرداخت</b>\n\n" + receiptBody(p, name)
}

func receiptBody(p *BotPayment, name string) string {
	paid := "—"
	if p.PaidAt != nil {
		paid = p.PaidAt.In(tehran).Format("2006-01-02 15:04")
	}
	return fmt.Sprintf("🏪 %s\n💰 مبلغ: <b>%s تومان</b>\n📌 وضعیت: %s\n🆔 سفارش: <code>%s</code>\n💳 کارت: <code>%s</code>\n🕒 پرداخت: %s\n🔗 tx: <code>%s</code>",
		html.EscapeString(name), formatToman(p.Payable), statusFa(p.Status),
		html.EscapeString(p.ID), html.EscapeString(p.Card), paid, html.EscapeString(p.TxID))
}

func statusFa(s string) string {
	switch s {
	case "pending":
		return "در انتظار پرداخت ⏳"
	case "paid":
		return "تایید شده ✅"
	case "expired":
		return "منقضی شده ⌛"
	case "canceled":
		return "لغو شده ❌"
	default:
		return s
	}
}

func parseCommand(text string) (string, string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", text
	}
	parts := strings.SplitN(text, " ", 2)
	cmd := strings.ToLower(parts[0])
	if i := strings.IndexByte(cmd, '@'); i >= 0 {
		cmd = cmd[:i]
	}
	arg := ""
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}
	return cmd, arg
}

func parseAmount(raw string) (int64, error) {
	s := normalizeDigits(strings.TrimSpace(raw))
	s = strings.ReplaceAll(s, "تومان", "")
	s = strings.NewReplacer(",", "", "٬", "", "،", "", " ", "", "_", "").Replace(s)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 1000 || n > 500_000_000 {
		return 0, errors.New("amount out of range")
	}
	return n, nil
}

func normalizeDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '۰' && r <= '۹':
			r = '0' + (r - '۰')
		case r >= '٠' && r <= '٩':
			r = '0' + (r - '٠')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func formatToman(n int64) string {
	s := strconv.FormatInt(n, 10)
	var out strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out.WriteByte(',')
		}
		out.WriteRune(c)
	}
	return out.String()
}
