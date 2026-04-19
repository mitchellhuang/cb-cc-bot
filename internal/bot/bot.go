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

type Bot struct {
	gmail    *gmail.Client
	coinbase *coinbase.Client
	telegram *telegram.Client
	cfg      *config.Config

	mu            sync.Mutex
	pendingAmount float64 // USD sell amount awaiting Telegram approval; 0 if none
}

func New(g *gmail.Client, cb *coinbase.Client, tg *telegram.Client, cfg *config.Config) *Bot {
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
			msgs, err := b.gmail.PollSince(ctx, watermark)
			if err != nil {
				log.Printf("gmail poll: %v", err)
				continue
			}
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
			"Your USDC balance (*$%.2f*) covers the upcoming Coinbase Card payment of *$%.2f*. No action needed.",
			usdcBalance, paymentAmount,
		)
		log.Printf("email %s: balance sufficient, notifying", msg.Id)
		if err := b.telegram.SendMessage(ctx, text); err != nil {
			log.Printf("telegram notify: %v", err)
		}
		return
	}

	btcPrice, err := b.coinbase.BTCPrice(ctx)
	if err != nil {
		log.Printf("email %s: get BTC price: %v", msg.Id, err)
		return
	}
	btcEstimate := sellAmount / btcPrice

	text := fmt.Sprintf(
		"*Coinbase Card autopay reminder*\n\nPayment due: *$%.2f*\nCurrent USDC balance: *$%.2f*\nAmount to sell: *$%.2f* (~%.6f BTC @ $%.2f/BTC)\n\nSell BTC to cover the difference?",
		paymentAmount, usdcBalance, sellAmount, btcEstimate, btcPrice,
	)
	if _, err := b.telegram.SendApprovalPrompt(ctx, text); err != nil {
		log.Printf("email %s: send approval prompt: %v", msg.Id, err)
		return
	}

	b.mu.Lock()
	b.pendingAmount = sellAmount
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
	b.mu.Unlock()

	if amount == 0 {
		log.Printf("callback %q: no pending approval, ignoring", cq.Data)
		return
	}

	switch cq.Data {
	case "reject":
		b.mu.Lock()
		b.pendingAmount = 0
		b.mu.Unlock()
		text := fmt.Sprintf("Sell skipped. Your USDC balance may not fully cover the autopay of *$%.2f*.", amount)
		if err := b.telegram.SendMessage(ctx, text); err != nil {
			log.Printf("telegram notify: %v", err)
		}

	case "approve":
		b.mu.Lock()
		b.pendingAmount = 0
		b.mu.Unlock()
		orderID, err := b.coinbase.MarketSellBTC(ctx, amount)
		if err != nil {
			log.Printf("market sell $%.2f: %v", amount, err)
			text := fmt.Sprintf("Failed to place market sell of *$%.2f*: %v", amount, err)
			b.telegram.SendMessage(ctx, text)
			return
		}
		log.Printf("market sell placed: order %s for $%.2f", orderID, amount)
		text := fmt.Sprintf("Market sell of *$%.2f* placed. Order ID: `%s`", amount, orderID)
		if err := b.telegram.SendMessage(ctx, text); err != nil {
			log.Printf("telegram notify: %v", err)
		}
	}
}
