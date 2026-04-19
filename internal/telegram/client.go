package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mitchellhuang/cb-cc-bot/internal/config"
)

type Client struct {
	http   *http.Client
	token  string
	chatID string
}

func NewClient(cfg *config.Config) *Client {
	return &Client{
		// Timeout must exceed the getUpdates long-poll duration (30s).
		http:   &http.Client{Timeout: 35 * time.Second},
		token:  cfg.TelegramBotToken,
		chatID: cfg.TelegramChatID,
	}
}

type Update struct {
	UpdateID      int            `json:"update_id"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type CallbackQuery struct {
	ID      string  `json:"id"`
	Data    string  `json:"data"`
	Message Message `json:"message"`
}

type Message struct {
	MessageID int    `json:"message_id"`
	Text      string `json:"text"`
}

// Ping verifies the bot token is valid via getMe.
func (c *Client) Ping(ctx context.Context) error {
	var resp struct {
		OK bool `json:"ok"`
	}
	if err := c.call(ctx, "getMe", url.Values{}, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("getMe returned ok=false")
	}
	return nil
}

// SendApprovalPrompt sends a message with Yes/No inline keyboard buttons.
func (c *Client) SendApprovalPrompt(ctx context.Context, text string) (int, error) {
	keyboard := map[string]any{
		"inline_keyboard": [][]map[string]string{
			{
				{"text": "Yes, sell", "callback_data": "approve"},
				{"text": "No, skip", "callback_data": "reject"},
			},
		},
	}
	kbJSON, err := json.Marshal(keyboard)
	if err != nil {
		return 0, err
	}

	params := url.Values{
		"chat_id":      {c.chatID},
		"text":         {text},
		"parse_mode":   {"Markdown"},
		"reply_markup": {string(kbJSON)},
	}

	var resp struct {
		OK     bool    `json:"ok"`
		Result Message `json:"result"`
	}
	if err := c.call(ctx, "sendMessage", params, &resp); err != nil {
		return 0, err
	}
	if !resp.OK {
		return 0, fmt.Errorf("sendMessage failed")
	}
	return resp.Result.MessageID, nil
}

// SendMessage sends a plain Markdown text notification.
func (c *Client) SendMessage(ctx context.Context, text string) error {
	params := url.Values{
		"chat_id":    {c.chatID},
		"text":       {text},
		"parse_mode": {"Markdown"},
	}
	var resp struct {
		OK bool `json:"ok"`
	}
	if err := c.call(ctx, "sendMessage", params, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("sendMessage failed")
	}
	return nil
}

// PollUpdates long-polls for Telegram updates and returns them along with the next offset.
func (c *Client) PollUpdates(ctx context.Context, offset int) ([]Update, int, error) {
	params := url.Values{
		"timeout": {"30"},
		"offset":  {strconv.Itoa(offset)},
	}
	var resp struct {
		OK     bool     `json:"ok"`
		Result []Update `json:"result"`
	}
	if err := c.call(ctx, "getUpdates", params, &resp); err != nil {
		return nil, offset, err
	}
	nextOffset := offset
	for _, u := range resp.Result {
		if u.UpdateID+1 > nextOffset {
			nextOffset = u.UpdateID + 1
		}
	}
	return resp.Result, nextOffset, nil
}

// AnswerCallbackQuery removes the loading spinner on the Telegram button.
func (c *Client) AnswerCallbackQuery(ctx context.Context, queryID string) error {
	params := url.Values{"callback_query_id": {queryID}}
	var resp struct {
		OK bool `json:"ok"`
	}
	return c.call(ctx, "answerCallbackQuery", params, &resp)
}

func (c *Client) call(ctx context.Context, method string, params url.Values, out any) error {
	u := fmt.Sprintf("https://api.telegram.org/bot%s/%s", c.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(params.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}
