package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	gmailapi "google.golang.org/api/gmail/v1"
)

// One-time Gmail OAuth2 authorization. Run locally on macOS to generate token.json,
// then store both credentials.json and token.json as Kubernetes Secrets.
func main() {
	credFile := getenv("GMAIL_CREDENTIALS_FILE", "credentials.json")
	tokenFile := getenv("GMAIL_TOKEN_FILE", "token.json")

	credBytes, err := os.ReadFile(credFile)
	if err != nil {
		log.Fatalf("read %s: %v", credFile, err)
	}

	cfg, err := google.ConfigFromJSON(credBytes, gmailapi.GmailReadonlyScope)
	if err != nil {
		log.Fatalf("parse credentials: %v", err)
	}
	cfg.RedirectURL = "http://localhost:8080/callback"

	token, err := runOAuthFlow(cfg)
	if err != nil {
		log.Fatalf("oauth flow: %v", err)
	}

	f, err := os.Create(tokenFile)
	if err != nil {
		log.Fatalf("create %s: %v", tokenFile, err)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(token); err != nil {
		log.Fatalf("write token: %v", err)
	}
	fmt.Printf("Token saved to %s\n", tokenFile)
}

func runOAuthFlow(cfg *oauth2.Config) (*oauth2.Token, error) {
	authURL := cfg.AuthCodeURL("state", oauth2.AccessTypeOffline)
	fmt.Printf("Open this URL in your browser:\n\n  %s\n\n", authURL)

	codeCh := make(chan string, 1)
	srv := &http.Server{Addr: ":8080"}
	http.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "Authorization complete. You can close this tab.")
		codeCh <- r.URL.Query().Get("code")
		go srv.Shutdown(context.Background())
	})
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return nil, fmt.Errorf("callback server: %w", err)
	}

	code := <-codeCh
	return cfg.Exchange(context.Background(), code)
}

func getenv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
