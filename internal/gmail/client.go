package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/mitchellhuang/cb-cc-bot/internal/config"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	gmailapi "google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

type Client struct {
	svc *gmailapi.Service
}

func NewClient(cfg *config.Config) (*Client, error) {
	credBytes, err := os.ReadFile(cfg.GmailCredentialsFile)
	if err != nil {
		return nil, fmt.Errorf("read credentials file: %w", err)
	}

	oauthCfg, err := google.ConfigFromJSON(credBytes, gmailapi.GmailReadonlyScope)
	if err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}

	token, err := loadToken(cfg.GmailTokenFile)
	if err != nil {
		return nil, fmt.Errorf("load token: %w", err)
	}

	ctx := context.Background()
	svc, err := gmailapi.NewService(ctx, option.WithTokenSource(oauthCfg.TokenSource(ctx, token)))
	if err != nil {
		return nil, fmt.Errorf("create gmail service: %w", err)
	}

	return &Client{svc: svc}, nil
}

// PollSince returns Coinbase autopay reminder emails received after the given time.
func (c *Client) PollSince(ctx context.Context, since time.Time) ([]*gmailapi.Message, error) {
	query := fmt.Sprintf(`from:noreply@creditcard.coinbase.com subject:"Reminder - Your automatic payment is coming up" after:%d`, since.Unix())

	resp, err := c.svc.Users.Messages.List("me").Q(query).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	if len(resp.Messages) == 0 {
		return nil, nil
	}

	msgs := make([]*gmailapi.Message, 0, len(resp.Messages))
	for _, m := range resp.Messages {
		full, err := c.svc.Users.Messages.Get("me", m.Id).Format("full").Context(ctx).Do()
		if err != nil {
			return nil, fmt.Errorf("get message %s: %w", m.Id, err)
		}
		msgs = append(msgs, full)
	}
	return msgs, nil
}

// Body extracts the plain-text body from a Gmail message.
func Body(msg *gmailapi.Message) (string, error) {
	return extractBody(msg.Payload)
}

func extractBody(part *gmailapi.MessagePart) (string, error) {
	if part == nil {
		return "", nil
	}
	if part.MimeType == "text/plain" && part.Body != nil && part.Body.Data != "" {
		data, err := base64.URLEncoding.DecodeString(part.Body.Data)
		if err != nil {
			return "", fmt.Errorf("decode body: %w", err)
		}
		return string(data), nil
	}
	for _, p := range part.Parts {
		if body, err := extractBody(p); err == nil && body != "" {
			return body, nil
		}
	}
	return "", nil
}

func loadToken(path string) (*oauth2.Token, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var token oauth2.Token
	if err := json.NewDecoder(f).Decode(&token); err != nil {
		return nil, err
	}
	return &token, nil
}
