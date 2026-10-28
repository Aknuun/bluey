# Bluey

تایید خودکار واریز کارت‌به‌کارت با **بلوبانک**، به‌همراه یک API ساده برای اتصال به ربات تلگرام، فروشگاه، سایت یا هر اپ دیگری.

هر پرداخت یک **مبلغ یکتا** می‌گیرد (مثلاً ۶۰۰٬۰۰۰ تومان → ۶۰۰٬۳۸۵ تومان). مشتری دقیقاً همان مبلغ را کارت‌به‌کارت می‌کند؛ سرویس با لاگین به نسخه وب بلوبانک، تراکنش‌های دریافتی را می‌خواند و پرداخت را تایید می‌کند.

> این پروژه متن‌باز است و روی سرور خودتان اجرا می‌شود. هیچ داده‌ای به سرور شخص ثالث فرستاده نمی‌شود.

---

## چطور کار می‌کند

```
ربات / سایت / اپ
      │  POST /v1/payments  (مبلغ + callback)
      ▼
   bluey ──────────────►  مبلغ یکتا + شماره کارت
      │                           │
      │                     مشتری کارت‌به‌کارت می‌کند
      │                           │
      ▼                           ▼
  مرورگر headless  ◄────  app.blubank.com
      │  خواندن تراکنش‌های دریافتی
      ▼
  تطبیق مبلغ + ثبت پرداخت
      │
      ▼
  webhook امضاشده  ──────►  ربات / سایت
```

- مبلغ یکتا: عدد پایه + پسوند ۱ تا ۹۹۹؛ فقط یک پرداخت باز می‌تواند هر مبلغ را داشته باشد.
- پرداخت‌های منقضی خودکار آزاد می‌شوند.
- تراکنش‌های تکراری با `tx_id` نادیده گرفته می‌شوند.
- واریزی‌های بدون پرداخت باز، برای بررسی دستی ذخیره می‌شوند.
- webhook با HMAC-SHA256 امضا و با backoff دوباره ارسال می‌شود.

---

## پیش‌نیازها

- یک سرور **ایرانی** که به `https://app.blubank.com` دسترسی دارد (از بعضی IPهای خارجی بسته است).
- Docker + Docker Compose.
- یک حساب بلوبانک و شماره کارت آن.
- گوشی همراه برای دریافت اولین کد OTP.

---

## راه‌اندازی سریع

```bash
git clone https://github.com/Aknuun/bluey.git
cd bluey

cp .env.example .env
# .env را ویرایش کنید: API_KEY، CARD_NUMBER، نام کاربری/رمز بلوبانک

docker compose up -d --build
docker compose logs -f
```

اگر سرور شما به بلوبانک دسترسی ندارد، `BLUBANK_PROXY` را روی یک پروکسی/تونل ایرانی تنظیم کنید.

---

## اولین ورود به بلوبانک (OTP)

بار اول، بلوبانک برای دستگاه جدید کد پیامکی می‌فرستد. سرویس وضعیت خود را در این endpoint نشان می‌دهد:

```bash
curl -s http://localhost:8080/v1/blubank/status \
  -H "X-API-Key: $API_KEY"
```

وقتی `state` برابر `otp_required` شد، کد پیامک‌شده را بفرستید:

```bash
curl -s -X POST http://localhost:8080/v1/blubank/otp \
  -H "X-API-Key: $API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"code":"123456"}'
```

- کد OTP معمولاً کمتر از دو دقیقه اعتبار دارد.
- اگر کد منقضی شد، سرویس را ری‌استارت کنید تا کد تازه بفرستد:
  ```bash
  docker compose restart bluey
  ```
- پروفایل مرورگر در `./data/blubank-profile` ذخیره می‌شود. این پوشه را پاک نکنید.
- نشست web (کلیدهای `sessionStorage`) در `BLUBANK_STATE_PATH` ذخیره می‌شود (پیش‌فرض: `./data/blubank-profile/session.json`). با همین فایل، ری‌استارت سرویس معمولاً **بدون OTP** انجام می‌شود؛ فقط اگر بانک نشست را باطل کند کد جدید لازم است.
- فایل نشست را مثل رمز عبور محافظت کنید (سرویس با دسترسی `0600` می‌سازد).

---

## API

همه درخواست‌ها به هدر `X-API-Key` نیاز دارند (به‌جز `/v1/health`).

### ساخت پرداخت

```bash
curl -s -X POST http://localhost:8080/v1/payments \
  -H "X-API-Key: $API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "amount": 600000,
    "unit": "toman",
    "order_id": "order-42",
    "callback_url": "https://example.com/webhook",
    "expires_in": 900
  }'
```

پاسخ:

```json
{
  "id": "pay_9f...",
  "order_id": "order-42",
  "amount": 600000,
  "unit": "toman",
  "amount_rial": 6000000,
  "payable_amount": 600385,
  "payable_amount_rial": 6003850,
  "status": "pending",
  "card_number": "6037-...",
  "expires_at": "2026-10-28T10:15:00Z"
}
```

به مشتری **`payable_amount`** را نشان دهید، نه `amount` را.

`unit` می‌تواند `toman` (پیش‌فرض) یا `rial` باشد. `expires_in` بین ۶۰ ثانیه و ۲۴ ساعت است.

### وضعیت پرداخت

```bash
curl -s http://localhost:8080/v1/payments/pay_9f... \
  -H "X-API-Key: $API_KEY"
```

`status`: `pending` | `paid` | `expired` | `canceled`

### لغو پرداخت

```bash
curl -s -X POST http://localhost:8080/v1/payments/pay_9f.../cancel \
  -H "X-API-Key: $API_KEY"
```

### تایید دستی

برای وقتی که خودتان واریز را دیدید:

```bash
curl -s -X POST http://localhost:8080/v1/payments/pay_9f.../confirm \
  -H "X-API-Key: $API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"tx_id":"manual-1","description":"تایید دستی"}'
```

### ثبت واریز از منبع دیگر (SMS، اپ، ...)

اگر منبع دیگری دارید، می‌توانید واریز را مستقیم ثبت کنید:

```bash
curl -s -X POST http://localhost:8080/v1/deposits \
  -H "X-API-Key: $API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"amount_rial":6003850,"tx_id":"bank-tx-123","description":"واریز"}'
```

`tx_id` باید یکتا باشد؛ برای همین endpoint اجباری است.

---

## Webhook

وقتی پرداخت تایید می‌شود، به `callback_url` یک `POST` با این بدنه می‌رود:

```json
{
  "id": "evt_...",
  "type": "payment.paid",
  "created_at": "2026-10-28T10:00:00Z",
  "data": {
    "payment_id": "pay_9f...",
    "order_id": "order-42",
    "amount": 600000,
    "unit": "toman",
    "payable_amount": 600385,
    "payable_amount_rial": 6003850,
    "paid_at": "2026-10-28T10:00:00Z",
    "transaction": {"id": "...", "description": "دریافت از کارت"}
  }
}
```

امضا:

```
X-Receipt-Signature: sha256=<hex hmac>
```

محاسبه: `HMAC-SHA256(raw_body, WEBHOOK_SECRET)`.

- پاسخ `2xx` = تحویل موفق.
- در غیر این صورت با backoff دوباره تلاش می‌شود (۱۰ ثانیه، ۳۰ ثانیه، ۱ دقیقه، ۵ دقیقه، ۳۰ دقیقه، سپس هر ۶ ساعت؛ حداکثر ۱۲ تلاش).
- برای idempotency از `id` رویداد یا `payment_id` استفاده کنید.

نمونه بررسی امضا در PHP: [`examples/mirza-webhook.php`](examples/mirza-webhook.php)

---

## اتصال به ربات میرزا

در میرزا، پرداخت کارت‌به‌کارت آفلاین یک `Payment_report` با وضعیت `waiting` می‌سازد و ادمین با `DirectPayment($order_id)` آن را تایید می‌کند.

برای خودکارسازی:

1. در ربات، هنگام انتخاب پرداخت، `POST /v1/payments` بزنید و `payable_amount` + `card_number` را نشان دهید.
2. `order_id` را همان شناسه سفارش میرزا بگذارید.
3. `callback_url` را به یک فایل PHP در ربات بدهید.
4. آن فایل بعد از بررسی امضا، `DirectPayment($order_id)` را صدا بزند و وضعیت `Payment_report` را `paid` کند.

اسکلت فایل در `examples/mirza-webhook.php` است. مسیر فایل‌های میرزا و نام ستون‌ها بین نسخه‌ها فرق می‌کند؛ بخش `// TODO` را با نسخه خودتان تطبیق دهید.

---

## اتصال به ربات/سایت دلخواه

- **ربات تلگرام:** بعد از ساخت پرداخت، مبلغ و کارت را بفرستید؛ وضعیت را با `GET /v1/payments/{id}` هر چند ثانیه چک کنید یا webhook را دریافت کنید. بعد از `paid`، کیف پول کاربر را شارژ کنید.
- **سایت:** همان API؛ چون هدر `X-API-Key` استفاده می‌شود، درخواست را از بک‌اند بزنید، نه از مرورگر.
- **اپ موبایل:** همان API؛ کلید را در اپ جاسازی نکنید، از بک‌اند خودتان استفاده کنید.

---

## امنیت

- `API_KEY` و `WEBHOOK_SECRET` را با `openssl rand -hex 32` بسازید.
- سرویس را پشت HTTPS (Nginx/Caddy) قرار دهید.
- از یک حساب/کارت **مجزا** با موجودی محدود استفاده کنید.
- `ALLOW_PRIVATE_CALLBACKS=false` را در تولید نگه دارید تا webhook به آدرس‌های داخلی (SSRF) نرود.
- رمز بلوبانک را فقط در `.env` روی سرور نگه دارید؛ `.env` در `.gitignore` است.
- فایل `BLUBANK_STATE_PATH` (نشست web) مثل رمز عبور است؛ آن را در گیت، بکاپ عمومی یا چت نگذارید.
- خودکارسازی نسخه وب بانک ممکن است خلاف شرایط استفاده باشد؛ مسئولیت آن با شماست.

---

## محدودیت‌ها

- یک حساب بلوبانک برای هر نمونه.
- نسخه وب بلوبانک نشست را حدود ۳۰ دقیقه نگه می‌دارد؛ سرویس خودکار دوباره لاگین می‌کند.
- ری‌استارت معمولاً بدون OTP است، چون نشست در `BLUBANK_STATE_PATH` ذخیره می‌شود؛ اگر بانک نشست را باطل کند یا فایل پاک شود، OTP جدید لازم است.
- خواندن تراکنش‌ها از DOM داشبورد انجام می‌شود؛ تغییر UI بانک ممکن است آن را بشکند.
- مبلغ باید دقیقاً مطابق `payable_amount` واریز شود؛ اشتباه در مبلغ یعنی عدم تطبیق خودکار.

---

## توسعه

```bash
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go run ./cmd/bluey
```

متغیرهای محیطی در `.env.example` آمده‌اند.

## مجوز

MIT
