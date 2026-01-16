package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

type PIAClient struct {
	config     *Config
	httpClient *http.Client
}

type SignatureResponse struct {
	Status    string `json:"status"`
	Message   string `json:"message"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

type PayloadData struct {
	Port      int    `json:"port"`
	ExpiresAt string `json:"expires_at"`
}

type BindResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// APIError represents an error response from the PIA API (non-OK status)
type APIError struct {
	Operation string
	Status    string
	Message   string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s failed: status=%s, message=%s", e.Operation, e.Status, e.Message)
	}
	return fmt.Sprintf("%s failed: status=%s", e.Operation, e.Status)
}

func NewPIAClient(config *Config) *PIAClient {
	// Create custom transport with interface binding
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// Get the interface
			iface, err := net.InterfaceByName("pia0")
			if err != nil {
				return nil, fmt.Errorf("failed to get pia interface: %w", err)
			}

			// Get addresses for the interface
			addrs, err := iface.Addrs()
			if err != nil {
				return nil, fmt.Errorf("failed to get interface addresses: %w", err)
			}

			if len(addrs) == 0 {
				return nil, fmt.Errorf("no addresses on pia interface")
			}

			// Use the first address
			ipNet, ok := addrs[0].(*net.IPNet)
			if !ok {
				return nil, fmt.Errorf("invalid address type")
			}

			// Create TCP address with the interface IP
			localAddr := &net.TCPAddr{
				IP: ipNet.IP,
			}

			// Dial with local address binding
			d := &net.Dialer{
				LocalAddr: localAddr,
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}

			return d.Dial(network, addr)
		},
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // -k flag in curl
		},
		MaxIdleConns:        10,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: transport,
	}

	return &PIAClient{
		config:     config,
		httpClient: client,
	}
}

func (c *PIAClient) GetSignature() (*SignatureResponse, error) {
	debugLog(c.config, "Requesting signature from %s", c.config.PFGateway)

	// Build URL with query parameters
	baseURL := fmt.Sprintf("https://%s:19999/getSignature", c.config.PFGateway)
	params := url.Values{}
	params.Add("token", c.config.Token)
	fullURL := baseURL + "?" + params.Encode()

	// Redact token in logs (show first 8 chars only)
	tokenPreview := c.config.Token
	if len(tokenPreview) > 8 {
		tokenPreview = tokenPreview[:8] + "..."
	}
	debugLog(c.config, "Executing: GET %s?token=%s", baseURL, tokenPreview)

	// Initial delay (matching bash behavior)
	time.Sleep(2 * time.Second)

	resp, err := c.httpClient.Get(fullURL)
	if err != nil {
		debugLog(c.config, "ERROR: Request failed: %v", err)
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		debugLog(c.config, "ERROR: Failed to read response: %v", err)
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	debugLog(c.config, "Response size: %d bytes", len(body))

	if len(body) == 0 {
		debugLog(c.config, "ERROR: Empty response from getSignature")
		return nil, fmt.Errorf("empty response")
	}

	var sigResp SignatureResponse
	if err := json.Unmarshal(body, &sigResp); err != nil {
		debugLog(c.config, "ERROR: Invalid JSON in response: %v", err)
		debugLog(c.config, "Response content: %s", string(body))
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	debugLog(c.config, "getSignature response - status: '%s'", sigResp.Status)

	if sigResp.Status != "OK" {
		return nil, &APIError{
			Operation: "getSignature",
			Status:    sigResp.Status,
			Message:   sigResp.Message,
		}
	}

	debugLog(c.config, "getSignature successful")
	return &sigResp, nil
}

func (c *PIAClient) GetSignatureWithRetry(ctx context.Context, retryDuration time.Duration) (*SignatureResponse, error) {
	var lastErr error
	backoff := 2 * time.Second
	maxBackoff := 30 * time.Second
	startTime := time.Now()
	attempt := 0

	for {
		attempt++

		// Check if context is cancelled
		if ctx.Err() != nil {
			debugLog(c.config, "GetSignatureWithRetry cancelled via context (attempt %d)", attempt)
			return nil, ctx.Err()
		}

		// Check if we've exceeded the retry duration
		if attempt > 1 && time.Since(startTime) >= retryDuration {
			debugLog(c.config, "Retry duration of %v exceeded after %d attempts", retryDuration, attempt-1)
			return nil, fmt.Errorf("failed after %v: %w", retryDuration, lastErr)
		}

		debugLog(c.config, "Signature attempt %d (elapsed: %v)", attempt, time.Since(startTime).Round(time.Second))

		if attempt > 1 {
			debugLog(c.config, "Waiting %v before retry...", backoff)

			// Use context-aware sleep
			select {
			case <-time.After(backoff):
				// Continue to retry
			case <-ctx.Done():
				debugLog(c.config, "Backoff sleep cancelled via context")
				return nil, ctx.Err()
			}

			// Exponential backoff with 30s cap
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}

		resp, err := c.GetSignature()
		if err == nil {
			return resp, nil
		}

		// Check if this is an API error (status != OK)
		// Don't retry API errors - need to reconnect instead
		if _, isAPIError := err.(*APIError); isAPIError {
			return nil, err
		}

		lastErr = err
		debugLog(c.config, "Attempt %d failed: %v", attempt, err)
	}
}

func (c *PIAClient) BindPort(payload, signature string) error {
	debugLog(c.config, "Calling bindPort")
	debugLog(c.config, "  Payload length: %d bytes", len(payload))
	debugLog(c.config, "  Signature length: %d bytes", len(signature))

	// Build URL with query parameters
	baseURL := fmt.Sprintf("https://%s:19999/bindPort", c.config.PFGateway)
	params := url.Values{}
	params.Add("payload", payload)
	params.Add("signature", signature)
	fullURL := baseURL + "?" + params.Encode()

	debugLog(c.config, "Executing: GET %s", baseURL)

	resp, err := c.httpClient.Get(fullURL)
	if err != nil {
		debugLog(c.config, "ERROR: Request failed: %v", err)
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		debugLog(c.config, "ERROR: Failed to read response: %v", err)
		return fmt.Errorf("failed to read response: %w", err)
	}

	debugLog(c.config, "Bind response size: %d bytes", len(body))

	if len(body) == 0 {
		debugLog(c.config, "ERROR: Empty response from bindPort")
		return fmt.Errorf("empty response")
	}

	var bindResp BindResponse
	if err := json.Unmarshal(body, &bindResp); err != nil {
		debugLog(c.config, "ERROR: Invalid JSON in response: %v", err)
		return fmt.Errorf("invalid JSON: %w", err)
	}

	debugLog(c.config, "bindPort response - status: '%s', message: '%s'", bindResp.Status, bindResp.Message)

	if bindResp.Status != "OK" {
		return &APIError{
			Operation: "bindPort",
			Status:    bindResp.Status,
			Message:   bindResp.Message,
		}
	}

	debugLog(c.config, "bindPort successful")
	return nil
}

func (c *PIAClient) BindPortWithRetry(ctx context.Context, payload, signature string, retryDuration time.Duration) error {
	var lastErr error
	backoff := 2 * time.Second
	maxBackoff := 30 * time.Second
	startTime := time.Now()
	attempt := 0

	for {
		attempt++

		// Check if context is cancelled
		if ctx.Err() != nil {
			debugLog(c.config, "BindPortWithRetry cancelled via context (attempt %d)", attempt)
			return ctx.Err()
		}

		// Check if we've exceeded the retry duration
		if attempt > 1 && time.Since(startTime) >= retryDuration {
			debugLog(c.config, "Retry duration of %v exceeded after %d attempts", retryDuration, attempt-1)
			showError(fmt.Sprintf("bindPort failed after %v: %v", retryDuration, lastErr))
			return fmt.Errorf("failed after %v: %w", retryDuration, lastErr)
		}

		debugLog(c.config, "Bind attempt %d (elapsed: %v)", attempt, time.Since(startTime).Round(time.Second))

		if attempt > 1 {
			debugLog(c.config, "Waiting %v before retry...", backoff)

			// Use context-aware sleep
			select {
			case <-time.After(backoff):
				// Continue to retry
			case <-ctx.Done():
				debugLog(c.config, "Backoff sleep cancelled via context")
				return ctx.Err()
			}

			// Exponential backoff with 30s cap
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}

		err := c.BindPort(payload, signature)
		if err == nil {
			return nil
		}

		// Check if this is an API error (status != OK)
		// Don't retry API errors - the signature/payload is the problem
		if _, isAPIError := err.(*APIError); isAPIError {
			showError(fmt.Sprintf("bindPort API error: %v", err))
			return err
		}

		lastErr = err
		debugLog(c.config, "Attempt %d failed: %v", attempt, err)
	}
}

// ParsePayload decodes the base64 payload and extracts port and expiry
func ParsePayload(payloadB64 string) (int, time.Time, error) {
	decoded, err := base64.StdEncoding.DecodeString(payloadB64)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("failed to decode payload: %w", err)
	}

	var payload PayloadData
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return 0, time.Time{}, fmt.Errorf("failed to parse payload JSON: %w", err)
	}

	// Parse ISO8601 timestamp (strip milliseconds if present)
	expiresStr := payload.ExpiresAt
	// Handle format: 2024-01-15T12:34:56.789Z -> 2024-01-15T12:34:56Z
	if len(expiresStr) > 20 && expiresStr[len(expiresStr)-1] == 'Z' {
		// Find the dot before milliseconds
		for i := len(expiresStr) - 2; i >= 0; i-- {
			if expiresStr[i] == '.' {
				expiresStr = expiresStr[:i] + "Z"
				break
			}
		}
	}

	expiresAt, err := time.Parse(time.RFC3339, expiresStr)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("failed to parse expires_at: %w", err)
	}

	return payload.Port, expiresAt, nil
}
