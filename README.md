# DR Sell Bot — Compact Go Project

This package is the same Go source project reorganized into at most 10 files.
No handler logic was intentionally changed during consolidation; the former
source files are grouped by responsibility.

## File layout (10 files total)

- `core.go` — application entry point, configuration, MongoDB connection/indexes.
- `handlers_user.go` — registration, user menus, sell orders, history, referrals.
- `handlers_admin.go` — admin orders, moderation/tools, advanced settings, parity handlers.
- `handlers_wallet.go` — wallet, atomic ledger operations, withdrawal worker.
- `handlers_support.go` — support tickets, persistent workflow session storage.
- `handlers_misc.go` — shared utilities, keyboards, integration commands/callbacks.
- `integration_handlers_test.go` — unit tests.
- `go.mod` — Go module and dependencies.
- `render.yaml` — Render native Go Web Service blueprint.

## Build and test

```sh
gofmt -w *.go
go mod tidy
go test ./...
go build -o ./bin/dr-sell-bot .
```

## Render deployment

Create a Blueprint from this repository or configure a Go Web Service with:

- Build: `mkdir -p bin && go mod tidy && go build -trimpath -o ./bin/dr-sell-bot .`
- Start: `./bin/dr-sell-bot`
- Health check: `/healthz`

Set `BOT_TOKEN`, `ADMIN_ID`, `MONGO_URL`, and `CHANNEL_ID`. Optional settings
include `BOT_USERNAME` (without `@`), `BINANCE_PAY`, `BYBIT_PAY`, `BEP20_ADDRESS`,
`MIN_AMOUNT`, and `MAX_AMOUNT`. Render supplies `PORT`.

## MongoDB and production safety

The database name and collection names are retained from the supplied Python
project. Wallet-critical operations require MongoDB transactions, so use MongoDB
Atlas or a replica-set/sharded deployment. First deploy with a staging Telegram
bot token and a staging database. Do not run Python and Go long-polling bots
with the same Telegram token at the same time.

## Verification status

The source consolidation is formatted and structurally checked. A successful
full compile/test is not claimed unless `go mod tidy`, `go test ./...`, and
`go build ./...` complete successfully in a network-enabled environment.


## Render build command

```sh
mkdir -p bin && go mod tidy && go build -trimpath -o ./bin/dr-sell-bot .
```

Start command:

```sh
./bin/dr-sell-bot
```

## Compile fixes from Render log

- Pass the callback sender as `&q.From` where a `*models.User` is required.
- Handle `CallbackQuery.Message` as `models.MaybeInaccessibleMessage`; only edit it when its embedded `Message` pointer is non-nil.
