package coinbase

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mitchellhuang/cb-cc-bot/internal/config"
	"github.com/golang-jwt/jwt/v5"
)

const baseURL = "https://api.coinbase.com/api/v3/brokerage"

type Client struct {
	http       *http.Client
	keyName    string
	privateKey *ecdsa.PrivateKey
}

func NewClient(cfg *config.Config) (*Client, error) {
	key, err := parseECKey(cfg.CoinbasePrivateKey)
	if err != nil {
		return nil, fmt.Errorf("parse coinbase private key: %w", err)
	}
	return &Client{
		http:       &http.Client{Timeout: 10 * time.Second},
		keyName:    cfg.CoinbaseAPIKeyName,
		privateKey: key,
	}, nil
}

// Ping verifies the API key is valid by fetching the accounts list.
func (c *Client) Ping(ctx context.Context) error {
	var resp struct {
		Accounts []struct{} `json:"accounts"`
	}
	return c.get(ctx, "/accounts", &resp)
}

// USDCBalance returns the available USDC balance.
func (c *Client) USDCBalance(ctx context.Context) (float64, error) {
	var resp struct {
		Accounts []struct {
			Currency         string `json:"currency"`
			AvailableBalance struct {
				Value string `json:"value"`
			} `json:"available_balance"`
		} `json:"accounts"`
	}
	if err := c.get(ctx, "/accounts", &resp); err != nil {
		return 0, err
	}
	for _, a := range resp.Accounts {
		if a.Currency == "USDC" {
			return strconv.ParseFloat(a.AvailableBalance.Value, 64)
		}
	}
	return 0, nil
}

// TakerFeeRate returns your current Advanced Trade taker fee rate (e.g. 0.006 for 0.6%).
// Market orders are always taker orders.
func (c *Client) TakerFeeRate(ctx context.Context) (float64, error) {
	var resp struct {
		FeeTier struct {
			TakerFeeRate string `json:"taker_fee_rate"`
		} `json:"fee_tier"`
	}
	if err := c.get(ctx, "/transaction_summary", &resp); err != nil {
		return 0, err
	}
	return strconv.ParseFloat(resp.FeeTier.TakerFeeRate, 64)
}

// BTCPrice returns the current BTC-USDC ask price.
func (c *Client) BTCPrice(ctx context.Context) (float64, error) {
	var resp struct {
		Pricebooks []struct {
			Asks []struct {
				Price string `json:"price"`
			} `json:"asks"`
		} `json:"pricebooks"`
	}
	if err := c.get(ctx, "/best_bid_ask?product_ids=BTC-USDC", &resp); err != nil {
		return 0, err
	}
	if len(resp.Pricebooks) == 0 || len(resp.Pricebooks[0].Asks) == 0 {
		return 0, fmt.Errorf("no BTC-USDC price available")
	}
	return strconv.ParseFloat(resp.Pricebooks[0].Asks[0].Price, 64)
}

// MarketSellBTC places a market sell order for the given BTC amount and returns the order ID.
// btcAmount is calculated by the caller as orderSizeUSD / currentBTCPrice.
// Coinbase market sells must be parameterized in base currency (BTC), not quote currency (USD).
func (c *Client) MarketSellBTC(ctx context.Context, btcAmount float64) (string, error) {
	body := map[string]any{
		"client_order_id": fmt.Sprintf("cb-cc-bot-%d", time.Now().UnixNano()),
		"product_id":      "BTC-USDC",
		"side":            "SELL",
		"order_configuration": map[string]any{
			"market_market_ioc": map[string]any{
				"base_size": fmt.Sprintf("%.8f", btcAmount),
			},
		},
	}
	var resp struct {
		Success         bool `json:"success"`
		SuccessResponse struct {
			OrderID string `json:"order_id"`
		} `json:"success_response"`
		ErrorResponse struct {
			Message string `json:"message"`
		} `json:"error_response"`
	}
	if err := c.post(ctx, "/orders", body, &resp); err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("order rejected: %s", resp.ErrorResponse.Message)
	}
	return resp.SuccessResponse.OrderID, nil
}

// OrderFill contains the fill details for a completed order.
type OrderFill struct {
	FilledBTC      float64
	FillPrice      float64
	USDCReceived   float64
	Fees           float64
}

// WaitForFill polls the order until it reaches a terminal state and returns fill details.
// Market orders on liquid pairs typically fill in under a second.
func (c *Client) WaitForFill(ctx context.Context, orderID string) (OrderFill, error) {
	for range 10 {
		fill, done, err := c.getOrderFill(ctx, orderID)
		if err != nil {
			return OrderFill{}, err
		}
		if done {
			return fill, nil
		}
		select {
		case <-ctx.Done():
			return OrderFill{}, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return OrderFill{}, fmt.Errorf("order %s did not fill within timeout", orderID)
}

func (c *Client) getOrderFill(ctx context.Context, orderID string) (OrderFill, bool, error) {
	var resp struct {
		Order struct {
			Status              string `json:"status"`
			FilledSize          string `json:"filled_size"`
			AverageFilledPrice  string `json:"average_filled_price"`
			TotalFees           string `json:"total_fees"`
			TotalValueAfterFees string `json:"total_value_after_fees"`
		} `json:"order"`
	}
	if err := c.get(ctx, "/orders/historical/"+orderID, &resp); err != nil {
		return OrderFill{}, false, err
	}

	switch resp.Order.Status {
	case "FILLED":
		filledBTC, _ := strconv.ParseFloat(resp.Order.FilledSize, 64)
		fillPrice, _ := strconv.ParseFloat(resp.Order.AverageFilledPrice, 64)
		fees, _ := strconv.ParseFloat(resp.Order.TotalFees, 64)
		usdcReceived, _ := strconv.ParseFloat(resp.Order.TotalValueAfterFees, 64)
		return OrderFill{
			FilledBTC:    filledBTC,
			FillPrice:    fillPrice,
			Fees:         fees,
			USDCReceived: usdcReceived,
		}, true, nil
	case "CANCELLED", "EXPIRED", "FAILED":
		return OrderFill{}, false, fmt.Errorf("order %s ended with status %s", orderID, resp.Order.Status)
	default:
		return OrderFill{}, false, nil
	}
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	token, err := c.buildJWT(method, path)
	if err != nil {
		return fmt.Errorf("build jwt: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var errBody map[string]any
		json.NewDecoder(resp.Body).Decode(&errBody)
		return fmt.Errorf("coinbase API %s %s: status %d: %v", method, path, resp.StatusCode, errBody)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// buildJWT creates a CDP-signed JWT for the given request per Coinbase Advanced Trade API docs.
func (c *Client) buildJWT(method, path string) (string, error) {
	// Strip query params from URI claim.
	uriPath := path
	if i := strings.Index(path, "?"); i != -1 {
		uriPath = path[:i]
	}

	now := time.Now()
	claims := jwt.MapClaims{
		"sub": c.keyName,
		"iss": "cdp",
		"nbf": now.Unix(),
		"exp": now.Add(2 * time.Minute).Unix(),
		"uri": fmt.Sprintf("%s api.coinbase.com/api/v3/brokerage%s", method, uriPath),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = c.keyName
	// nonce required by Coinbase to prevent replay attacks
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	token.Header["nonce"] = fmt.Sprintf("%x", nonce)

	return token.SignedString(c.privateKey)
}

func parseECKey(pemStr string) (*ecdsa.PrivateKey, error) {
	// Env vars passed via kubectl --from-literal or shell exports may contain
	// literal \n instead of real newlines. Normalize before PEM decoding.
	pemStr = strings.ReplaceAll(pemStr, `\n`, "\n")
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}
	// Try SEC1 EC key format first, then PKCS8.
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	ecKey, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("key is not ECDSA")
	}
	return ecKey, nil
}
