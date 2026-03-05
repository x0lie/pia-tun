package portforward

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type signatureResponse struct {
	Status    string `json:"status"`
	Message   string `json:"message"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

type payloadData struct {
	Port      int    `json:"port"`
	ExpiresAt string `json:"expires_at"`
}

type bindResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type apiError struct {
	operation string
	status    string
	message   string
}

func (e *apiError) Error() string {
	if e.message != "" {
		return fmt.Sprintf("%s failed: status=%s, message=%s", e.operation, e.status, e.message)
	}
	return fmt.Sprintf("%s failed: status=%s", e.operation, e.status)
}

func (m *manager) getSignature(ctx context.Context) (*signatureResponse, error) {
	token := m.connCfg.Token
	baseURL := fmt.Sprintf("https://%s:%v/getSignature", m.connCfg.PFGateway, pfAPIPort)
	params := url.Values{}
	params.Add("token", token)
	fullURL := baseURL + "?" + params.Encode()

	// Create preview for sanitization
	tokenPreview := m.connCfg.Token
	if len(tokenPreview) > 8 {
		tokenPreview = tokenPreview[:8] + "..."
	}
	m.log.Debug("Executing: GET %s?token=%s", baseURL, tokenPreview)

	req, _ := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	resp, err := m.httpClient.Do(req)
	if err != nil {
		// Sanitize token from error before logging
		errMsg := strings.ReplaceAll(err.Error(), url.QueryEscape(token), url.QueryEscape(tokenPreview))
		return nil, errors.New(errMsg)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if len(body) == 0 {
		return nil, fmt.Errorf("empty response")
	}

	var sigResp signatureResponse
	if err := json.Unmarshal(body, &sigResp); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	m.log.Trace("getSignature response - status: '%s'", sigResp.Status)

	if sigResp.Status != "OK" {
		return nil, &apiError{
			operation: "getSignature",
			status:    sigResp.Status,
			message:   sigResp.Message,
		}
	}

	m.log.Debug("getSignature successful")
	return &sigResp, nil
}

func (m *manager) bindPort(ctx context.Context, payload, signature string) error {
	baseURL := fmt.Sprintf("https://%s:%v/bindPort", m.connCfg.PFGateway, pfAPIPort)
	params := url.Values{}
	params.Add("payload", payload)
	params.Add("signature", signature)
	fullURL := baseURL + "?" + params.Encode()

	// Create previews for sanitization
	payloadPreview := payload
	if len(payloadPreview) > 16 {
		payloadPreview = payloadPreview[:16] + "..."
	}
	sigPreview := signature
	if len(sigPreview) > 12 {
		sigPreview = sigPreview[:12] + "..."
	}

	m.log.Trace("Executing: GET %s", baseURL)

	req, _ := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	resp, err := m.httpClient.Do(req)
	if err != nil {
		// Sanitize payload and signature from error
		errMsg := err.Error()
		errMsg = strings.ReplaceAll(errMsg, url.QueryEscape(payload), url.QueryEscape(payloadPreview))
		errMsg = strings.ReplaceAll(errMsg, url.QueryEscape(signature), url.QueryEscape(sigPreview))
		return fmt.Errorf("request failed: %s", errMsg)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if len(body) == 0 {
		return fmt.Errorf("empty response from bindport")
	}

	var bindResp bindResponse
	if err := json.Unmarshal(body, &bindResp); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	m.log.Trace("bindPort response - status: '%s', message: '%s'", bindResp.Status, bindResp.Message)

	if bindResp.Status != "OK" {
		return &apiError{
			operation: "bindPort",
			status:    bindResp.Status,
			message:   bindResp.Message,
		}
	}

	return nil
}

func (m *manager) getSignatureWithRetry(ctx context.Context) (*signatureResponse, error) {
	var resp *signatureResponse
	err := m.retryWithDeadline(ctx, "getSignature", func() error {
		var err error
		resp, err = m.getSignature(ctx)
		return err
	})
	return resp, err
}

func (m *manager) bindPortWithRetry(ctx context.Context, payload, signature string) error {
	return m.retryWithDeadline(ctx, "bindPort", func() error {
		err := m.bindPort(ctx, payload, signature)
		if err == nil {
			m.state.bindTime = time.Now()
		}
		return err
	})
}

func (m *manager) retryWithDeadline(ctx context.Context, operation string, fn func() error) error {
	deadline := m.state.bindTime.Add(portBindDuration)
	retryCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	startTime := time.Now()

	for {
		m.log.Trace("Attempting %s (elapsed: %v)", operation, time.Since(startTime).Round(time.Second))

		err := fn()
		if err == nil {
			return nil
		}

		if _, isAPIError := err.(*apiError); isAPIError {
			return fmt.Errorf("%s API error: %w", operation, err)
		}

		m.log.Debug("%s attempt failed: %v", operation, err)
		m.log.Debug("Waiting %v before retry...", retryInterval)

		select {
		case <-time.After(retryInterval):
		case <-retryCtx.Done():
			if errors.Is(retryCtx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("failed to %s after %v - port bind expiring soon", operation, time.Since(startTime).Round(time.Second))
			}
			m.log.Debug("Received shutdown signal")
			return ctx.Err()
		}
	}
}

func parsePayload(payloadB64 string) (int, time.Time, error) {
	decoded, err := base64.StdEncoding.DecodeString(payloadB64)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("failed to decode payload: %w", err)
	}

	var payload payloadData
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return 0, time.Time{}, fmt.Errorf("failed to parse payload JSON: %w", err)
	}

	expiresStr := payload.ExpiresAt
	// PIA sends fractional seconds that time.RFC3339 cannot parse
	if len(expiresStr) > 20 && expiresStr[len(expiresStr)-1] == 'Z' {
		expiresStr = strings.SplitN(expiresStr, ".", 2)[0] + "Z"
	}

	expiresAt, err := time.Parse(time.RFC3339, expiresStr)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("failed to parse expires_at: %w", err)
	}

	return payload.Port, expiresAt, nil
}
