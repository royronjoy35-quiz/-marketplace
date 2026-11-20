# TaskVerse — Flat Go Web Service

এই ZIP-টি আপলোড করা আগের TaskVerse starter-এর এক-সার্ভিস সংস্করণ। সব Go source file project root-এ রাখা হয়েছে।

## ফাইল বিন্যাস

```text
taskverse/
├── main.go
├── config.go
├── mongodb.go
├── handlers.go
├── telegram.go
├── go.mod
├── Dockerfile
├── render.yaml
├── .env.example
└── .gitignore
```

## Render port সম্পর্কে

Render Web Service-এ একটি HTTP port-এ bind করা বাধ্যতামূলক। Render `PORT` environment variable দেয়। অ্যাপ সেটি ব্যবহার করে `0.0.0.0`-এ listen করে; `PORT` অনুপস্থিত থাকলে local fallback `10000` ব্যবহার করে। এটি fake port নয়—Render health check এবং Telegram webhook গ্রহণের জন্য প্রয়োজনীয় HTTP listener।

Render Dashboard-এ:
- Service type: **Web Service**
- Runtime: **Docker**
- Dockerfile: `./Dockerfile`
- Health Check Path: `/healthz`

## Environment variables

Render-এ `.env.example`-এর মানগুলো সেট করুন। `BOT_TOKEN`, `MONGODB_URI`, `REDIS_URL` এবং `TELEGRAM_WEBHOOK_SECRET` secret হিসেবে দিন। `.env` কখনো GitHub-এ commit করবেন না।

Telegram webhook URL:
`https://YOUR-SERVICE.onrender.com/telegram/webhook`

Telegram-এর `setWebhook`-এ একই `TELEGRAM_WEBHOOK_SECRET`-কে `secret_token` হিসেবে দিন।

## বর্তমান ZIP-এ যা কাজ করে

- Go HTTP health endpoint (`/healthz`)
- Telegram webhook endpoint
- MongoDB ও Redis connection এবং health check
- HTTP request timeout, সীমিত concurrent webhook processing, MongoDB/Redis connection pool
- Telegram message text/caption এবং Telegram entity দেখে Link/URL প্রত্যাখ্যান
- graceful shutdown
- Render Docker deployment configuration
- payout configuration flags default-এ disabled

## গুরুত্বপূর্ণ সীমাবদ্ধতা

এটি এখনো পূর্ণ TaskVerse marketplace নয়। Task creation, task submission, verification, Stars payment, wallet ledger, withdrawals, referral, admin dashboard এবং বাস্তব USDT transfer engine এখানে implement করা হয়নি। Telegram handler-এ আপাতত `/start` ও সাধারণ acknowledgement আছে।

১০,০০০–৫০,০০০ user-এর capacity কেবল এই starter দেখে নিশ্চিত করা যায় না। বাস্তব load test, database indexes, idempotency, queue-based processing, monitoring, rate limits এবং একাধিক instance-এর shared coordination দরকার। Render instance scale করলে in-memory concurrency limit প্রতি instance-এ আলাদা হবে।

BEP20 payout এখনো চালু নয়। `PAYOUTS_ENABLED=false` এবং `MAINNET_PAYOUTS_ENABLED=false` রাখুন। কোনো private key GitHub, chat, MongoDB বা সাধারণ source file-এ রাখবেন না। Production payout-এর আগে isolated signer/KMS, nonce control, receipt verification, idempotency, reconciliation ও security audit প্রয়োজন।
