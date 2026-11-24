## Admin roles and universal search (updated 2026-11-24)

- The configured `ADMIN_ID` is shown separately as **Permanent Admin / Owner**.
- The Manage Administrators view lists all Sub-Admins, including Active and Suspended entries.
- Only the Permanent Admin can add, suspend, or reactivate Sub-Admins.
- New administrator entries are stored with `role: sub_admin`; active status controls access.
- Both Permanent Admin and active Sub-Admins see **🔎 Search Records** in the dashboard.

Universal Search accepts:
- `order:123456` — Sell Order ID
- `withdraw:123456` — Withdrawal ID
- `user:123456789` — User ID and related orders, withdrawals, and tickets
- `mobile:01712345678` — Mobile number
- `file:AgAC...` — Telegram File ID
- `trx:ABC123...` — Transaction / TRX ID, including audit history
- `ticket:TICK-123-456` — Support Ticket ID

A plain numeric search checks matching Order, Withdrawal, User, and linked records. A plain non-numeric search checks relevant transaction, file, user, and ticket fields. Results are read-only; existing workflow dashboards continue to provide approval/rejection controls.

# DR Sell Bot — Compact Go Edition

This compact project keeps the source tree to 10 files. The six Go source files contain the runtime, user, admin, wallet, support, and utility workflows.

## Render Native Go Web Service

Build Command:

```sh
go mod tidy && go mod download && go build -trimpath -o dr-sell-bot .
```

Start Command:

```sh
./dr-sell-bot
```

Health check path: `/healthz`. Render supplies `PORT`; the HTTP health server binds to it. No `bin/` directory is used.

## Required Environment Variables

- `BOT_TOKEN`
- `ADMIN_ID`
- `MONGO_URL`
- `CHANNEL_ID`

Optional:
- `BOT_USERNAME` (without `@`)
- `BINANCE_PAY`
- `BYBIT_PAY`
- `BEP20_ADDRESS`
- `MIN_AMOUNT` (default `0.2`)
- `MAX_AMOUNT` (default `25.0`)

## Database Compatibility

The original Python `database.py` uses database `ⒹⓇsellbot` and collections:
`sell_orders`, `sell_users`, `sell_bot_settings`, `sell_tickets`, `sell_admins`,
`sell_audit_logs`, `sell_user_reviews`, and `sell_withdraw_requests`.
The Go code retains these names and uses additional collections for workflow sessions and the wallet ledger.

Use a MongoDB replica set or Atlas deployment for wallet-critical multi-document transactions. Test against a staging database first.

## Image-based UI

The image URLs and message wording are ported from the supplied Python `config.py` and `handlers.py`. Menus use the matching image banner with the reply/inline keyboard; long captions are split into a banner followed by the full text. If Telegram cannot fetch a configured image URL, the handler falls back to text so the workflow remains usable.

The bot keeps the Python reply-keyboard labels and the corresponding menu routes. It does not add a new Welcome banner because the supplied Python `start()` sends the welcome text with the main reply keyboard and does not configure a welcome-photo constant.

## Recharge workflow (ported from supplied Python handlers.py)

### Sell payout via Recharge
1. Admin approves a sell order and the user selects `Recharge`.
2. The user enters a Bangladesh mobile number (`01[3-9]xxxxxxxx`); the input message is deleted when possible.
3. The bot displays operator choices: `GP`, `Skito`, `Teletalk`, `Robi`, `Banglalink`, `Circle`.
4. The selected operator is appended to the payout method (for example `Recharge (Robi)`).
5. The order changes to `approved_pending_payment`; the admin receives the pending-payment notification with order, amount, number, and payment actions.
6. Recharge has no fixed ৳5 payout fee.

### Wallet withdrawal via Recharge
1. The user chooses Wallet Withdrawal and `Recharge`.
2. The fixed fee is ৳0; the amount follows the existing BDT rounding and wallet-adjustment calculation.
3. The user enters a Bangladesh mobile number, then chooses one of the six operators above.
4. The selected operator is stored with the method and the withdrawal request is submitted for admin processing.
5. The withdrawal uses the existing wallet reservation, paid, and rejection/refund workflow.

### Other payout methods
bKash, Nagad, Rocket, and Upay retain Bangladesh mobile-number validation and saved-number support. Bangla QR retains photo upload handling.

## Smart Reply and Support

The support message router is called before other admin input sessions so an active ticket reply is not consumed by a different admin prompt. Text and supported media are forwarded to the selected ticket's user and recorded in ticket history. Ticket state and workflow sessions are persisted in MongoDB.

## Verification status

- `gofmt` formatting is run on all Go files in this package.
- ZIP structure is checked after packaging.
- Full `go mod tidy`, `go test ./...`, and `go build ./...` could not be completed in this environment because DNS resolution for `proxy.golang.org` is blocked. Render must perform the dependency download and compile.
- Live Telegram photo URL fetching, callbacks, recharge operator delivery, and MongoDB transaction/restart flows must be verified on a staging bot/database before production cutover.

Do not run the Python and Go bots simultaneously with the same Telegram token. Keep the Python bot available until staging tests pass.

## Permanent Admin, Sub-Admins, and Universal Search

- `ADMIN_ID` is the single Permanent Admin / Owner. It is displayed separately in the Admin Management Dashboard and cannot be suspended from the Sub-Admin list.
- Users added through **Manage Administrators → Add Sub-Admin** are stored in `sell_admins` with `role: sub_admin` and `status: active`.
- The Permanent Admin can suspend or reactivate Sub-Admins. Suspended Sub-Admins no longer pass the `IsAdmin` check.
- Both Permanent Admin and active Sub-Admins have the **🔎 Search Records** dashboard entry.
- Search accepts plain values or explicit prefixes:
  - `order:123456` — Sell Order ID
  - `withdraw:123456` — Withdrawal ID
  - `user:123456789` — User ID and linked records
  - `mobile:01712345678` — Mobile number
  - `file:AgAC...` — Telegram file ID
  - `trx:ABC123...` — Transaction / TRX ID
  - `ticket:TICK-123-456` — Support Ticket ID
- A plain numeric ID searches matching order, withdrawal, user, and related records. A plain non-numeric value searches matching transaction/file/user/ticket fields.
- Search results are read-only summaries; existing pending-order, withdrawal, and support dashboards retain their action buttons.

## Admin Search Data Coverage

Search checks `sell_orders`, `sell_withdraw_requests`, `sell_users`, `sell_tickets`, and `sell_audit_logs`. File-ID matching checks common proof, payout, withdrawal, and support attachment fields. TRX lookup checks order/withdrawal transaction fields and audit history.
