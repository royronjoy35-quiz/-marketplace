# Render Environment Variables

একটি Render Web Service-এ নিচের environment variables দিন:

| Key | Value |
|---|---|
| APP_NAME | TaskVerse |
| APP_ENV | production |
| BOT_TOKEN | BotFather থেকে পাওয়া token (Secret) |
| TELEGRAM_WEBHOOK_SECRET | শক্তিশালী random secret (Secret) |
| ADMIN_IDS | 7695907394 |
| MONGODB_URI | MongoDB connection URI (Secret) |
| MONGODB_DATABASE | taskverse |
| REDIS_URL | Redis connection URI (Secret) |
| MIN_PAYOUT_USDT | 5 |
| MAX_PAYOUT_USDT | 10 |
| DAILY_PAYOUT_LIMIT_USDT | 25 |
| BSC_CHAIN_ID | 97 |
| PAYOUTS_ENABLED | false |
| MAINNET_PAYOUTS_ENABLED | false |

`PORT` Render নিজে সরবরাহ করে। সাধারণত Dashboard-এ আলাদা করে সেট করার দরকার নেই। অ্যাপ `PORT` পড়বে এবং `0.0.0.0`-এ bind করবে।

## Telegram webhook

Webhook URL হবে `https://YOUR-SERVICE.onrender.com/telegram/webhook`। Telegram `setWebhook` request-এ `secret_token` হিসেবে `TELEGRAM_WEBHOOK_SECRET`-এর একই মান দিন।

কোনো token, database URI বা secret GitHub-এ commit করবেন না।
