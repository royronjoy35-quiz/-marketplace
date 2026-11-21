# DR Marketplace Bot — Flat Go Web Service

## Deployment
- Root directory: repository root (leave blank in Render).
- Build command: `go mod tidy && go build -o app .`
- Start command: `./app`
- Runtime: Render Web Service; listens on `0.0.0.0:$PORT`.
- Required environment: `BOT_TOKEN`, `BOT_USERNAME` (`dr_marketplace_bot`), `TELEGRAM_WEBHOOK_SECRET` (32+ chars), `PUBLIC_BASE_URL` (HTTPS, e.g. `https://marketplace-drfb.onrender.com`), `MONGODB_URI`, `ADMIN_IDS` (comma-separated Telegram numeric IDs).
- Optional: `DATABASE_NAME` (default `dr_marketplace`), `WEBHOOK_PATH` (default `/webhook/telegram`), `PORT` (Render supplies this).

## Roles and keyboards
- User, Worker, Seller and Admin receive separate persistent reply keyboards.
- The Bot API sends `is_persistent=true` and `one_time_keyboard=false` for reply keyboards.
- The four-square keyboard toggle icon is drawn by the Telegram client. A bot can request a persistent custom keyboard but cannot force the client to show that exact icon or its position.

## Task workflow
1. Seller/Admin creates a task through the guided four-step prompt.
2. Seller tasks wait for Admin approval; Admin-created tasks are published immediately.
3. A task remains closed to applications until an Admin manually verifies its external BEP-20 escrow transfer and records it using `/mark_task_funded TASK_ID TX_HASH`.
4. Users apply using the inline Apply button on a funded task.
5. Seller/Admin accepts or rejects applications using inline buttons.
6. Accepted users receive the Worker keyboard and submit text, URL, or a Telegram photo as proof.
7. Seller/Admin reviews submissions using Approve, Revision, or Reject buttons.
8. Approved rewards are recorded as `pending_funding` in `earnings_ledger`. They are not withdrawable until a separate funded ledger/payout process is implemented.

## Admin commands
- `/admin`
- `/mark_task_funded TASK_ID TX_HASH` — only after checking the BEP-20 transfer independently.
- `/ban USER_ID`, `/unban USER_ID`, `/suspend USER_ID`, `/restore USER_ID`
- `/approve_task TASK_ID`, `/reject_task TASK_ID`
- `/pause_task TASK_ID`, `/resume_task TASK_ID`, `/delete_task TASK_ID`
- `/set_auto_withdraw_threshold AMOUNT`

## Safety and current limitations
- The bot does not hold or request a wallet private key or seed phrase.
- The bot does not independently verify or broadcast BEP-20 transactions. Funding status is an Admin attestation with a recorded hash.
- Task reward ledger entries are pending funding, not withdrawable.
- Text, URL and photo proof are supported; video/document proof and revision notes are not yet implemented.
- A database unique index prevents duplicate applications by the same worker to the same task.
- Existing tasks created before this update have no `funding_status`; they remain closed until Admin records funding after independent verification.
