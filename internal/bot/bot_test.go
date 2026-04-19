package bot

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/mitchellhuang/cb-cc-bot/internal/config"
	"github.com/mitchellhuang/cb-cc-bot/internal/telegram"
	gmailapi "google.golang.org/api/gmail/v1"
)

// -- mocks --

type mockCoinbase struct {
	usdcBalance float64
	btcPrice    float64
	orderID     string
	sellCalled  bool
	sellAmount  float64
}

func (m *mockCoinbase) USDCBalance(_ context.Context) (float64, error) { return m.usdcBalance, nil }
func (m *mockCoinbase) BTCPrice(_ context.Context) (float64, error)    { return m.btcPrice, nil }
func (m *mockCoinbase) MarketSellBTC(_ context.Context, usd float64) (string, error) {
	m.sellCalled = true
	m.sellAmount = usd
	return m.orderID, nil
}

type mockTelegram struct {
	messages []string
	prompts  []string
}

func (m *mockTelegram) SendMessage(_ context.Context, text string) error {
	m.messages = append(m.messages, text)
	return nil
}
func (m *mockTelegram) SendApprovalPrompt(_ context.Context, text string) (int, error) {
	m.prompts = append(m.prompts, text)
	return 1, nil
}
func (m *mockTelegram) PollUpdates(_ context.Context, offset int) ([]telegram.Update, int, error) {
	return nil, offset, nil
}
func (m *mockTelegram) AnswerCallbackQuery(_ context.Context, _ string) error { return nil }

// -- helpers --

func makeMessage(id, body string) *gmailapi.Message {
	return &gmailapi.Message{
		Id: id,
		Payload: &gmailapi.MessagePart{
			MimeType: "text/plain",
			Body: &gmailapi.MessagePartBody{
				Data: base64.URLEncoding.EncodeToString([]byte(body)),
			},
		},
	}
}

func newBot(cb *mockCoinbase, tg *mockTelegram, maxSell float64) *Bot {
	return &Bot{
		coinbase: cb,
		telegram: tg,
		cfg:      &config.Config{MaxSellUSD: maxSell, PollInterval: time.Minute},
	}
}

const realEmailBody = "Your automatic payment of $6,840.03 is scheduled for April 4"

// -- handleEmail tests --

func TestHandleEmail_BalanceSufficient(t *testing.T) {
	cb := &mockCoinbase{usdcBalance: 7000.00}
	tg := &mockTelegram{}
	b := newBot(cb, tg, 10000)

	b.handleEmail(context.Background(), makeMessage("msg1", realEmailBody))

	if len(tg.prompts) != 0 {
		t.Error("expected no approval prompt when balance is sufficient")
	}
	if len(tg.messages) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(tg.messages))
	}
	if !strings.Contains(tg.messages[0], "No action needed") {
		t.Errorf("unexpected message: %s", tg.messages[0])
	}
	if cb.sellCalled {
		t.Error("sell should not have been called")
	}
}

func TestHandleEmail_BalanceCoversExactly(t *testing.T) {
	cb := &mockCoinbase{usdcBalance: 6840.03}
	tg := &mockTelegram{}
	b := newBot(cb, tg, 10000)

	b.handleEmail(context.Background(), makeMessage("msg1", realEmailBody))

	if len(tg.prompts) != 0 {
		t.Error("expected no approval prompt when balance exactly covers payment")
	}
}

func TestHandleEmail_PartialBalance_SendsPrompt(t *testing.T) {
	cb := &mockCoinbase{usdcBalance: 1000.00, btcPrice: 50000.00}
	tg := &mockTelegram{}
	b := newBot(cb, tg, 10000)

	b.handleEmail(context.Background(), makeMessage("msg1", realEmailBody))

	if len(tg.prompts) != 1 {
		t.Fatalf("expected 1 approval prompt, got %d", len(tg.prompts))
	}
	// sell amount should be $5,840.03 (6840.03 - 1000.00)
	if !strings.Contains(tg.prompts[0], "5840.03") {
		t.Errorf("prompt missing expected sell amount: %s", tg.prompts[0])
	}
	if b.pendingAmount != 5840.03 {
		t.Errorf("pendingAmount = %.2f, want 5840.03", b.pendingAmount)
	}
}

func TestHandleEmail_NoBalance_SendsPromptForFullAmount(t *testing.T) {
	cb := &mockCoinbase{usdcBalance: 0, btcPrice: 50000.00}
	tg := &mockTelegram{}
	b := newBot(cb, tg, 10000)

	b.handleEmail(context.Background(), makeMessage("msg1", realEmailBody))

	if len(tg.prompts) != 1 {
		t.Fatalf("expected 1 approval prompt, got %d", len(tg.prompts))
	}
	if b.pendingAmount != 6840.03 {
		t.Errorf("pendingAmount = %.2f, want 6840.03", b.pendingAmount)
	}
}

func TestHandleEmail_ExceedsMaxSell_Blocked(t *testing.T) {
	cb := &mockCoinbase{usdcBalance: 0}
	tg := &mockTelegram{}
	b := newBot(cb, tg, 5000) // max is $5000, payment is $6840.03

	b.handleEmail(context.Background(), makeMessage("msg1", realEmailBody))

	if len(tg.prompts) != 0 {
		t.Error("expected no approval prompt when sell is blocked")
	}
	if len(tg.messages) != 1 {
		t.Fatalf("expected 1 block notification, got %d", len(tg.messages))
	}
	if !strings.Contains(tg.messages[0], "blocked") {
		t.Errorf("unexpected message: %s", tg.messages[0])
	}
	if b.pendingAmount != 0 {
		t.Error("pendingAmount should remain 0 when blocked")
	}
}

func TestHandleEmail_PendingApproval_Skipped(t *testing.T) {
	cb := &mockCoinbase{usdcBalance: 0, btcPrice: 50000}
	tg := &mockTelegram{}
	b := newBot(cb, tg, 10000)
	b.pendingAmount = 999.00 // simulate existing pending approval

	b.handleEmail(context.Background(), makeMessage("msg2", realEmailBody))

	if len(tg.prompts) != 0 {
		t.Error("expected no new prompt while approval is pending")
	}
}

// -- handleCallback tests --

func TestHandleCallback_Approve_ExecutesSell(t *testing.T) {
	cb := &mockCoinbase{orderID: "order-123"}
	tg := &mockTelegram{}
	b := newBot(cb, tg, 10000)
	b.pendingAmount = 5840.03

	b.handleCallback(context.Background(), &telegram.CallbackQuery{ID: "cq1", Data: "approve"})

	if !cb.sellCalled {
		t.Fatal("expected MarketSellBTC to be called")
	}
	if cb.sellAmount != 5840.03 {
		t.Errorf("sell amount = %.2f, want 5840.03", cb.sellAmount)
	}
	if b.pendingAmount != 0 {
		t.Error("pendingAmount should be cleared after approval")
	}
	if len(tg.messages) != 1 || !strings.Contains(tg.messages[0], "order-123") {
		t.Errorf("expected confirmation message with order ID, got: %v", tg.messages)
	}
}

func TestHandleCallback_Reject_SkipsSell(t *testing.T) {
	cb := &mockCoinbase{}
	tg := &mockTelegram{}
	b := newBot(cb, tg, 10000)
	b.pendingAmount = 5840.03

	b.handleCallback(context.Background(), &telegram.CallbackQuery{ID: "cq1", Data: "reject"})

	if cb.sellCalled {
		t.Error("sell should not be called on reject")
	}
	if b.pendingAmount != 0 {
		t.Error("pendingAmount should be cleared after rejection")
	}
	if len(tg.messages) != 1 || !strings.Contains(tg.messages[0], "skipped") {
		t.Errorf("expected skip notification, got: %v", tg.messages)
	}
}

func TestHandleCallback_NoPending_Ignored(t *testing.T) {
	cb := &mockCoinbase{}
	tg := &mockTelegram{}
	b := newBot(cb, tg, 10000)
	// pendingAmount is 0

	b.handleCallback(context.Background(), &telegram.CallbackQuery{ID: "cq1", Data: "approve"})

	if cb.sellCalled {
		t.Error("sell should not be called when there is no pending approval")
	}
}
