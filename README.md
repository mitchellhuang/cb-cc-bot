# cb-cc-bot

Automates a BTC → USDC market sell on Coinbase Advanced Trade when a Coinbase credit card autopay reminder arrives in Gmail.

**Why:** Coinbase's default credit card autopay uses Simple Trade when your USDC balance is low, which charges significantly higher fees than Advanced Trade. This bot watches for the reminder email, checks your USDC balance, and — with your approval via Telegram — pre-funds the balance by selling BTC on Advanced Trade before the autopay executes.

**Flow:**
1. Polls Gmail every 5 minutes for the Coinbase autopay reminder email
2. Checks your current USDC balance on Coinbase Advanced Trade
3. If balance already covers the payment, sends a Telegram notification and stops
4. Otherwise, sends a Telegram message showing the math (payment amount, current balance, amount to sell) with a **Yes / No** button
5. On approval, places a market sell order (BTC-USDC) for the difference

## Prerequisites

- Go 1.23+
- A [Google Cloud project](https://console.cloud.google.com) with the Gmail API enabled and an OAuth2 Desktop App credential
- A [Coinbase Advanced Trade](https://advanced.coinbase.com) account with a CDP API key (EC key pair)
- A Telegram bot token from [@BotFather](https://t.me/BotFather)

## One-time setup

### 1. Gmail OAuth2

Download `credentials.json` from Google Cloud Console (OAuth 2.0 Client ID → Desktop App), then run the auth helper locally:

```bash
GMAIL_CREDENTIALS_FILE=credentials.json GMAIL_TOKEN_FILE=token.json go run cmd/auth/main.go
```

This opens a browser for Google login and writes `token.json`. The refresh token inside it is long-lived and survives pod restarts. You only need to redo this if you revoke access in your Google account settings.

### 2. Coinbase CDP API key

In Coinbase Advanced Trade → API settings, create a new CDP API key scoped to **View** and **Trade**. Save the key name (format: `organizations/.../apiKeys/...`) and the EC private key PEM.

### 3. Telegram bot and chat ID

1. Message [@BotFather](https://t.me/BotFather) → `/newbot` → save the token
2. Send any message to your new bot, then call:
   ```
   https://api.telegram.org/bot<TOKEN>/getUpdates
   ```
   and find your `chat.id` in the response.

## Running locally

```bash
export GMAIL_CREDENTIALS_FILE=credentials.json
export GMAIL_TOKEN_FILE=token.json
export COINBASE_API_KEY_NAME=organizations/.../apiKeys/...
export COINBASE_PRIVATE_KEY="-----BEGIN EC PRIVATE KEY-----\n..."
export TELEGRAM_BOT_TOKEN=...
export TELEGRAM_CHAT_ID=...

go run cmd/bot/main.go
```

## Kubernetes deployment

Store secrets and run as a single-replica Deployment:

```bash
kubectl create secret generic cb-cc-bot-secrets \
  --from-file=credentials.json \
  --from-file=token.json \
  --from-literal=COINBASE_API_KEY_NAME=... \
  --from-literal=COINBASE_PRIVATE_KEY=... \
  --from-literal=TELEGRAM_BOT_TOKEN=... \
  --from-literal=TELEGRAM_CHAT_ID=...
```

## Configuration

| Variable | Default | Description |
|---|---|---|
| `GMAIL_CREDENTIALS_FILE` | `credentials.json` | Path to Gmail OAuth2 credentials |
| `GMAIL_TOKEN_FILE` | `token.json` | Path to Gmail OAuth2 token |
| `COINBASE_API_KEY_NAME` | — | Coinbase CDP API key name |
| `COINBASE_PRIVATE_KEY` | — | Coinbase CDP EC private key (PEM) |
| `TELEGRAM_BOT_TOKEN` | — | Telegram bot token |
| `TELEGRAM_CHAT_ID` | — | Telegram chat ID to send messages to |
| `POLL_INTERVAL` | `5m` | How often to poll Gmail |
