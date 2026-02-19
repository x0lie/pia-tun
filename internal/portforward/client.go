package portforward

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/pia"
)

// PIAClient handles PIA port forwarding API requests.
type PIAClient struct {
	config     *Config
	connConfig *ConnectionConfig
	log        *log.Logger
	httpClient *http.Client
}

// SignatureResponse is the PIA getSignature API response.
type SignatureResponse struct {
	Status    string `json:"status"`
	Message   string `json:"message"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

// PayloadData is the decoded payload from a signature response.
type PayloadData struct {
	Port      int    `json:"port"`
	ExpiresAt string `json:"expires_at"`
}

// BindResponse is the PIA bindPort API response.
type BindResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// APIError represents an error response from the PIA API (non-OK status).
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

// NewPIAClient creates a new PIA port forwarding client bound to the pia0 interface.
func NewPIAClient(config *Config, connConfig *ConnectionConfig, logger *log.Logger) *PIAClient {
	return &PIAClient{
		config:     config,
		connConfig: connConfig,
		log:        logger,
		httpClient: pia.NewBoundClient(10*time.Second, 10*time.Second),
	}
}

func (c *PIAClient) GetSignature() (*SignatureResponse, error) {
	c.log.Debug("Requesting signature from %s", c.connConfig.PFGateway)

	token := c.connConfig.Token
	baseURL := fmt.Sprintf("https://%s:19999/getSignature", c.connConfig.PFGateway)
	params := url.Values{}
	params.Add("token", token)
	fullURL := baseURL + "?" + params.Encode()

	tokenPreview := c.connConfig.Token
	if len(tokenPreview) > 8 {
		tokenPreview = tokenPreview[:8] + "..."
	}
	c.log.Debug("Executing: GET %s?token=%s", baseURL, tokenPreview)

	resp, err := c.httpClient.Get(fullURL)
	if err != nil {
		c.log.Debug("ERROR: Request failed: %v", err)
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.log.Debug("ERROR: Failed to read response: %v", err)
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	c.log.Debug("Response size: %d bytes", len(body))

	if len(body) == 0 {
		c.log.Debug("ERROR: Empty response from getSignature")
		return nil, fmt.Errorf("empty response")
	}

	var sigResp SignatureResponse
	if err := json.Unmarshal(body, &sigResp); err != nil {
		c.log.Debug("ERROR: Invalid JSON in response: %v", err)
		c.log.Debug("Response content: %s", string(body))
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	c.log.Debug("getSignature response - status: '%s'", sigResp.Status)

	if sigResp.Status != "OK" {
		return nil, &APIError{
			Operation: "getSignature",
			Status:    sigResp.Status,
			Message:   sigResp.Message,
		}
	}

	c.log.Debug("getSignature successful")
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

		if ctx.Err() != nil {
			c.log.Debug("GetSignatureWithRetry cancelled via context (attempt %d)", attempt)
			return nil, ctx.Err()
		}

		if attempt > 1 && time.Since(startTime) >= retryDuration {
			c.log.Debug("Retry duration of %v exceeded after %d attempts", retryDuration, attempt-1)
			return nil, fmt.Errorf("failed after %v: %w", retryDuration, lastErr)
		}

		c.log.Debug("Signature attempt %d (elapsed: %v)", attempt, time.Since(startTime).Round(time.Second))

		if attempt > 1 {
			c.log.Debug("Waiting %v before retry...", backoff)

			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				c.log.Debug("Backoff sleep cancelled via context")
				return nil, ctx.Err()
			}

			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}

		resp, err := c.GetSignature()
		if err == nil {
			return resp, nil
		}

		if _, isAPIError := err.(*APIError); isAPIError {
			return nil, err
		}

		lastErr = err
		c.log.Debug("Attempt %d failed: %v", attempt, err)
	}
}

func (c *PIAClient) BindPort(payload, signature string) error {
	c.log.Trace("Calling bindPort")
	c.log.Trace("  Payload length: %d bytes", len(payload))
	c.log.Trace("  Signature length: %d bytes", len(signature))

	baseURL := fmt.Sprintf("https://%s:19999/bindPort", c.connConfig.PFGateway)
	params := url.Values{}
	params.Add("payload", payload)
	params.Add("signature", signature)
	fullURL := baseURL + "?" + params.Encode()

	c.log.Trace("Executing: GET %s", baseURL)

	resp, err := c.httpClient.Get(fullURL)
	if err != nil {
		c.log.Debug("ERROR: Request failed: %v", err)
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.log.Debug("ERROR: Failed to read response: %v", err)
		return fmt.Errorf("failed to read response: %w", err)
	}

	c.log.Trace("Bind response size: %d bytes", len(body))

	if len(body) == 0 {
		c.log.Debug("ERROR: Empty response from bindPort")
		return fmt.Errorf("empty response")
	}

	var bindResp BindResponse
	if err := json.Unmarshal(body, &bindResp); err != nil {
		c.log.Debug("ERROR: Invalid JSON in response: %v", err)
		return fmt.Errorf("invalid JSON: %w", err)
	}

	c.log.Trace("bindPort response - status: '%s', message: '%s'", bindResp.Status, bindResp.Message)

	if bindResp.Status != "OK" {
		return &APIError{
			Operation: "bindPort",
			Status:    bindResp.Status,
			Message:   bindResp.Message,
		}
	}

	c.log.Trace("bindPort successful")
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

		if ctx.Err() != nil {
			c.log.Debug("BindPortWithRetry cancelled via context (attempt %d)", attempt)
			return ctx.Err()
		}

		if attempt > 1 && time.Since(startTime) >= retryDuration {
			c.log.Debug("Retry duration of %v exceeded after %d attempts", retryDuration, attempt-1)
			log.Error(fmt.Sprintf("bindPort failed after %v: %v", retryDuration, lastErr))
			return fmt.Errorf("failed after %v: %w", retryDuration, lastErr)
		}

		c.log.Trace("Bind attempt %d (elapsed: %v)", attempt, time.Since(startTime).Round(time.Second))

		if attempt > 1 {
			c.log.Debug("Waiting %v before retry...", backoff)

			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				c.log.Debug("Backoff sleep cancelled via context")
				return ctx.Err()
			}

			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}

		err := c.BindPort(payload, signature)
		if err == nil {
			return nil
		}

		if _, isAPIError := err.(*APIError); isAPIError {
			log.Error(fmt.Sprintf("bindPort API error: %v", err))
			return err
		}

		lastErr = err
		c.log.Debug("Attempt %d failed: %v", attempt, err)
	}
}

// ParsePayload decodes the base64 payload and extracts port and expiry.
func ParsePayload(payloadB64 string) (int, time.Time, error) {
	decoded, err := base64.StdEncoding.DecodeString(payloadB64)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("failed to decode payload: %w", err)
	}

	var payload PayloadData
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return 0, time.Time{}, fmt.Errorf("failed to parse payload JSON: %w", err)
	}

	expiresStr := payload.ExpiresAt
	if len(expiresStr) > 20 && expiresStr[len(expiresStr)-1] == 'Z' {
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
