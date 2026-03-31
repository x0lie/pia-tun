package portsync

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/x0lie/pia-tun/internal/log"
)

type transmission struct {
	url    string
	user   string
	pass   string
	client *http.Client
	log    *log.Logger
}

func newTransmission(apiURL, user, pass string, logger *log.Logger) *transmission {
	return &transmission{
		url:  apiURL,
		user: user,
		pass: pass,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		log: logger,
	}
}

func (t *transmission) Name() string { return "transmission" }

func (t *transmission) SyncPort(ctx context.Context, port int) error {
	// Step 1: Get session ID from the RPC endpoint
	sessionID, err := t.getSessionID(ctx)
	if err != nil {
		return fmt.Errorf("get session ID: %w", err)
	}

	// Step 2: Set the peer port via RPC
	if err := t.setPort(ctx, sessionID, port); err != nil {
		return fmt.Errorf("set port: %w", err)
	}

	return nil
}

func (t *transmission) getSessionID(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		t.url+"/transmission/rpc", nil)
	if err != nil {
		return "", err
	}

	if t.user != "" {
		req.SetBasicAuth(t.user, t.pass)
	}

	// Transmission returns 409 with the session ID in the response body
	resp, err := t.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	t.log.Debug("getSessionID response: status=%d body=%.200s", resp.StatusCode, string(body))

	// Extract session ID from response body
	// Format: X-Transmission-Session-Id: <value>
	bodyStr := string(body)
	marker := "X-Transmission-Session-Id: "
	idx := strings.Index(bodyStr, marker)
	if idx == -1 {
		return "", fmt.Errorf("session ID not found in response")
	}

	// Extract the value (ends at < or newline)
	value := bodyStr[idx+len(marker):]
	if end := strings.IndexAny(value, "<\n\r"); end != -1 {
		value = value[:end]
	}
	value = strings.TrimSpace(value)

	if value == "" {
		return "", fmt.Errorf("empty session ID")
	}

	t.log.Debug("session ID obtained: %s", value)
	return value, nil
}

func (t *transmission) setPort(ctx context.Context, sessionID string, port int) error {
	body := fmt.Sprintf(`{"method":"session-set","arguments":{"peer-port":%d}}`, port)

	req, err := http.NewRequestWithContext(ctx, "POST",
		t.url+"/transmission/rpc",
		strings.NewReader(body))
	if err != nil {
		return err
	}

	if t.user != "" {
		req.SetBasicAuth(t.user, t.pass)
	}
	req.Header.Set("X-Transmission-Session-Id", sessionID)
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	t.log.Debug("setPort response: status=%d body=%s", resp.StatusCode, string(respBody))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	if !strings.Contains(string(respBody), `"result":"success"`) {
		return fmt.Errorf("no success in response: %s", string(respBody))
	}

	return nil
}
