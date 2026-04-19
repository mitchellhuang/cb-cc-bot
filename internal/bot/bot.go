package bot

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/mitchellhuang/cb-cc-bot/internal/coinbase"
	"github.com/mitchellhuang/cb-cc-bot/internal/config"
	"github.com/mitchellhuang/cb-cc-bot/internal/email"
	"github.com/mitchellhuang/cb-cc-bot/internal/gmail"
	"github.com/mitchellhuang/cb-cc-bot/internal/telegram"
	gmailapi "google.golang.org/api/gmail/v1"
)

type gmailPoller interface {
	PollSince(ctx context.Context, since time.Time) ([]*gmailapi.Message, error)
}

type coinbaseClient interface {
	USDCBalance(ctx context.Context) (float64, error)
	TakerFeeRate(ctx context.Context) (float64, error)
	BTCPrice(ctx context.Context) (float64, error)
	MarketSellBTC(ctx context.Context, btcAmount float64) (string, error)
	WaitForFill(ctx context.Context, orderID string) (coinbase.OrderFill, error)
}

type telegramClient interface {
	SendMessage(ctx context.Context, text string) error
	SendApprovalPrompt(ctx context.Context, text string) (int, error)
	PollUpdates(ctx context.Context, offset int) ([]telegram.Update, int, error)
	AnswerCallbackQuery(ctx context.Context, queryID string) error
}

type Bot struct {
	gmail    gmailPoller
	coinbase coinbaseClient
	telegram telegramClient
	cfg      *config.Config

	mu             sync.Mutex
	pendingAmount  float64 // fee-adjusted USD order size awaiting Telegram approval; 0 if none
	pendingBTC     float64 // BTC amount to sell, derived from pendingAmount / btcPrice at prompt time
}

func New(g gmailPoller, cb coinbaseClient, tg telegramClient, cfg *config.Config) *Bot {
	return &Bot{gmail: g, coinbase: cb, telegram: tg, cfg: cfg}
}

func (b *Bot) Run(ctx context.Context) error {
	watermark := time.Now()
	log.Printf("bot started: processing emails arriving after %s", watermark.Format(time.RFC3339))

	tgOffset := 0
	go b.telegramLoop(ctx, &tgOffset)

	ticker := time.NewTicker(b.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			log.Printf("polling gmail for new emails since %s", watermark.Format(time.RFC3339))
			msgs, err := b.gmail.PollSince(ctx, watermark)
			if err != nil {
				log.Printf("gmail poll: %v", err)
				continue
			}
			log.Printf("poll complete: %d matching email(s) found", len(msgs))
			watermark = time.Now()
			for _, msg := range msgs {
				b.handleEmail(ctx, msg)
			}
		}
	}
}

func (b *Bot) handleEmail(ctx context.Context, msg *gmailapi.Message) {
	b.mu.Lock()
	pending := b.pendingAmount
	b.mu.Unlock()
	if pending != 0 {
		log.Printf("email %s: skipped — approval already pending for $%.2f", msg.Id, pending)
		return
	}

	body, err := gmail.Body(msg)
	if err != nil {
		log.Printf("email %s: extract body: %v", msg.Id, err)
		return
	}

	paymentAmount, err := email.ParsePaymentAmount(body)
	if err != nil {
		log.Printf("email %s: parse amount: %v", msg.Id, err)
		return
	}
	log.Printf("email %s: payment amount $%.2f", msg.Id, paymentAmount)

	usdcBalance, err := b.coinbase.USDCBalance(ctx)
	if err != nil {
		log.Printf("email %s: get USDC balance: %v", msg.Id, err)
		return
	}

	sellAmount := max(0.0, paymentAmount-usdcBalance)

	if sellAmount == 0 {
		text := fmt.Sprintf(
			"*Coinbase Card autopay reminder*\n\nPayment due: *$%.2f*\nCurrent USDC balance: *$%.2f*\n\nYour balance covers the payment in full. No action needed.",
			paymentAmount, usdcBalance,
		)
		log.Printf("email %s: balance sufficient, notifying", msg.Id)
		if err := b.telegram.SendMessage(ctx, text); err != nil {
			log.Printf("telegram notify: %v", err)
		}
		return
	}

	if sellAmount > b.cfg.MaxSellUSD {
		text := fmt.Sprintf(
			"*Coinbase Card autopay reminder*\n\nPayment due: *$%.2f*\nCurrent USDC balance: *$%.2f*\nRequired sell: *$%.2f*\n\nSell blocked: required amount exceeds the configured limit of *$%.2f*. Manual action required.",
			paymentAmount, usdcBalance, sellAmount, b.cfg.MaxSellUSD,
		)
		log.Printf("email %s: sell amount $%.2f exceeds MAX_SELL_USD $%.2f, blocked", msg.Id, sellAmount, b.cfg.MaxSellUSD)
		if err := b.telegram.SendMessage(ctx, text); err != nil {
			log.Printf("telegram notify: %v", err)
		}
		return
	}

	feeRate, err := b.coinbase.TakerFeeRate(ctx)
	if err != nil {
		log.Printf("email %s: get taker fee rate: %v", msg.Id, err)
		return
	}
	// Inflate for taker fee, then add slippage buffer to cover price movement at fill time.
	orderSize := sellAmount / (1 - feeRate) * (1 + b.cfg.SlippageBuffer)
	feeAmount := (sellAmount / (1 - feeRate)) - sellAmount
	slippageAmount := orderSize - (sellAmount / (1 - feeRate))

	btcPrice, err := b.coinbase.BTCPrice(ctx)
	if err != nil {
		log.Printf("email %s: get BTC price: %v", msg.Id, err)
		return
	}
	btcEstimate := orderSize / btcPrice

	text := fmt.Sprintf(
		"*Coinbase Card autopay reminder*\n\nPayment due: *$%.2f*\nCurrent USDC balance: *$%.2f*\nNeeded: *$%.2f*\nAdv. Trade fee (~%.2f%%): *+$%.2f*\nSlippage buffer (%.2f%%): *+$%.2f*\nOrder size: *$%.2f* (~%.6f BTC @ $%.2f/BTC)\n\nSell BTC to cover the difference?",
		paymentAmount, usdcBalance, sellAmount, feeRate*100, feeAmount, b.cfg.SlippageBuffer*100, slippageAmount, orderSize, btcEstimate, btcPrice,
	)
	if _, err := b.telegram.SendApprovalPrompt(ctx, text); err != nil {
		log.Printf("email %s: send approval prompt: %v", msg.Id, err)
		return
	}

	b.mu.Lock()
	b.pendingAmount = orderSize
	b.pendingBTC = btcEstimate
	b.mu.Unlock()
}

func (b *Bot) telegramLoop(ctx context.Context, offset *int) {
	for ctx.Err() == nil {
		updates, newOffset, err := b.telegram.PollUpdates(ctx, *offset)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("telegram poll: %v", err)
				time.Sleep(5 * time.Second)
			}
			continue
		}
		*offset = newOffset
		for _, u := range updates {
			if u.CallbackQuery != nil {
				b.handleCallback(ctx, u.CallbackQuery)
			}
		}
	}
}

func (b *Bot) handleCallback(ctx context.Context, cq *telegram.CallbackQuery) {
	if err := b.telegram.AnswerCallbackQuery(ctx, cq.ID); err != nil {
		log.Printf("answer callback query: %v", err)
	}

	b.mu.Lock()
	amount := b.pendingAmount
	btcAmount := b.pendingBTC
	b.mu.Unlock()

	if amount == 0 {
		log.Printf("callback %q: no pending approval, ignoring", cq.Data)
		return
	}

	switch cq.Data {
	case "reject":
		b.mu.Lock()
		b.pendingAmount = 0
		b.pendingBTC = 0
		b.mu.Unlock()
		text := fmt.Sprintf("Sell skipped. Your USDC balance may not fully cover the autopay of *$%.2f*.", amount)
		if err := b.telegram.SendMessage(ctx, text); err != nil {
			log.Printf("telegram notify: %v", err)
		}

	case "approve":
		b.mu.Lock()
		b.pendingAmount = 0
		b.pendingBTC = 0
		b.mu.Unlock()
		orderID, err := b.coinbase.MarketSellBTC(ctx, btcAmount)
		if err != nil {
			log.Printf("market sell %.8f BTC: %v", btcAmount, err)
			text := fmt.Sprintf("Failed to place market sell of *%.8f BTC*: %v", btcAmount, err)
			b.telegram.SendMessage(ctx, text)
			return
		}
		log.Printf("market sell placed: order %s for %.8f BTC (~$%.2f)", orderID, btcAmount, amount)
		if err := b.telegram.SendMessage(ctx, fmt.Sprintf("Order placed. Waiting for fill... Order ID: `%s`", orderID)); err != nil {
			log.Printf("telegram notify: %v", err)
		}

		fill, err := b.coinbase.WaitForFill(ctx, orderID)
		if err != nil {
			log.Printf("wait for fill %s: %v", orderID, err)
			b.telegram.SendMessage(ctx, fmt.Sprintf("Could not confirm fill for order `%s`: %v", orderID, err))
			return
		}

		usdcBalance, err := b.coinbase.USDCBalance(ctx)
		if err != nil {
			log.Printf("post-fill USDC balance: %v", err)
		}

		log.Printf("order %s filled: %.8f BTC @ $%.2f, received $%.2f after $%.2f fees", orderID, fill.FilledBTC, fill.FillPrice, fill.USDCReceived, fill.Fees)
		text := fmt.Sprintf(
			"*Order filled*\n\nSold: *%.8f BTC* @ $%.2f\nFees: *$%.2f*\nUSDC received: *$%.2f*\nNew USDC balance: *$%.2f*",
			fill.FilledBTC, fill.FillPrice, fill.Fees, fill.USDCReceived, usdcBalance,
		)
		if err := b.telegram.SendMessage(ctx, text); err != nil {
			log.Printf("telegram notify: %v", err)
		}
	}
}
