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

func NewPIAClient(config *Config) *PIAClient {
	// Create custom transport with interface binding
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// Get the interface
			iface, err := net.InterfaceByName("pia")
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

	debugLog(c.config, "Executing: GET %s", baseURL)

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

	debugLog(c.config, "getSignature status: '%s'", sigResp.Status)

	if sigResp.Status != "OK" {
		debugLog(c.config, "Non-OK status '%s' from getSignature", sigResp.Status)
		return nil, fmt.Errorf("non-OK status: %s", sigResp.Status)
	}

	debugLog(c.config, "getSignature successful")
	return &sigResp, nil
}

func (c *PIAClient) GetSignatureWithRetry(ctx context.Context, maxRetries int) (*SignatureResponse, error) {
	var lastErr error
	backoff := 2 * time.Second

	for attempt := 1; attempt <= maxRetries; attempt++ {
		// Check if context is cancelled
		if ctx.Err() != nil {
			debugLog(c.config, "GetSignatureWithRetry cancelled via context (attempt %d/%d)", attempt, maxRetries)
			return nil, ctx.Err()
		}

		debugLog(c.config, "Signature attempt %d/%d", attempt, maxRetries)

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

			backoff *= 2 // Exponential backoff
		}

		resp, err := c.GetSignature()
		if err == nil {
			return resp, nil
		}

		lastErr = err
		debugLog(c.config, "Attempt %d failed: %v", attempt, err)
	}

	debugLog(c.config, "All %d attempts failed", maxRetries)
	return nil, fmt.Errorf("failed after %d attempts: %w", maxRetries, lastErr)
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

	debugLog(c.config, "bindPort status: '%s'", bindResp.Status)

	if bindResp.Status != "OK" {
		debugLog(c.config, "ERROR: bindPort failed with status '%s'", bindResp.Status)
		debugLog(c.config, "Response content: %s", string(body))
		return fmt.Errorf("bindPort failed: %s", bindResp.Status)
	}

	return nil
}

func (c *PIAClient) BindPortWithRetry(ctx context.Context, payload, signature string, maxRetries int) error {
	var lastErr error
	backoff := 2 * time.Second

	for attempt := 1; attempt <= maxRetries; attempt++ {
		// Check if context is cancelled
		if ctx.Err() != nil {
			debugLog(c.config, "BindPortWithRetry cancelled via context (attempt %d/%d)", attempt, maxRetries)
			return ctx.Err()
		}

		debugLog(c.config, "Bind attempt %d/%d", attempt, maxRetries)

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

			backoff *= 2 // Exponential backoff
		}

		err := c.BindPort(payload, signature)
		if err == nil {
			return nil
		}

		lastErr = err
		debugLog(c.config, "Attempt %d failed: %v", attempt, err)
	}

	debugLog(c.config, "All %d attempts failed", maxRetries)
	return fmt.Errorf("failed after %d attempts: %w", maxRetries, lastErr)
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
