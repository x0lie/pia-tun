package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"syscall"
	"time"
)

type KeepaliveManager struct {
	config *Config
	client *PIAClient
	state  *State
	mu     sync.Mutex
}

type State struct {
	Port                  int
	Payload               string
	Signature             string
	ExpiresAt             time.Time
	LastSignatureTime     time.Time
	BindCount             int
	SignatureRefreshCount int
}

func NewKeepaliveManager(config *Config, client *PIAClient) *KeepaliveManager {
	return &KeepaliveManager{
		config: config,
		client: client,
		state:  &State{},
	}
}

func (m *KeepaliveManager) Run(ctx context.Context) error {
	// Initial signature acquisition
	if err := m.initialSetup(); err != nil {
		return err
	}

	// Mark port forwarding as complete
	os.WriteFile("/tmp/port_forwarding_complete", []byte(""), 0644)
	debugLog(m.config, "Port forwarding setup complete, entering main loop")

	// Main keepalive loop
	return m.keepaliveLoop(ctx)
}

func (m *KeepaliveManager) initialSetup() error {
	showInfo()
	showStep("Acquiring port forward signature...")
	debugLog(m.config, "Starting initial signature acquisition (max retries: 5)")

	// Use background context for initial setup (no cancellation during startup)
	ctx := context.Background()

	// Try to get signature with retries (5 minutes for initial setup)
	resp, err := m.client.GetSignatureWithRetry(ctx, 5*time.Minute)
	if err != nil {
		showError("Port forwarding failed after 5 minutes")
		debugLog(m.config, "Exhausted all initial signature attempts, giving up")
		showVPNConnectedWarning()

		// Block forever (matching bash behavior)
		select {}
	}

	// Parse the response
	debugLog(m.config, "Parsing initial signature response...")
	port, expiresAt, err := ParsePayload(resp.Payload)
	if err != nil {
		showError(fmt.Sprintf("Failed to parse signature: %v", err))
		debugLog(m.config, "Payload parsing failed: %v", err)
		showVPNConnectedWarning()
		select {}
	}

	debugLog(m.config, "Initial parsed values:")
	debugLog(m.config, "  Port: %d", port)
	debugLog(m.config, "  Payload length: %d bytes", len(resp.Payload))
	debugLog(m.config, "  Signature length: %d bytes", len(resp.Signature))
	debugLog(m.config, "  Expires at: %d (%s)", expiresAt.Unix(), expiresAt.Format("2006-01-02 15:04:05"))

	if port == 0 {
		showError("Failed to extract port from response")
		debugLog(m.config, "PORT is zero after parsing")
		showVPNConnectedWarning()
		select {}
	}

	// Store state
	m.mu.Lock()
	m.state.Port = port
	m.state.Payload = resp.Payload
	m.state.Signature = resp.Signature
	m.state.ExpiresAt = expiresAt
	m.state.LastSignatureTime = time.Now()
	m.mu.Unlock()

	// Initial bind (3 minutes)
	debugLog(m.config, "Performing initial bind...")
	if err := m.client.BindPortWithRetry(ctx, resp.Payload, resp.Signature, 3*time.Minute); err != nil {
		showWarning("Initial bind failed, but continuing...")
		debugLog(m.config, "Initial bind failure is non-fatal, continuing with port announcement")
	} else {
		debugLog(m.config, "Initial bind successful")
	}

	// Write port to file
	debugLog(m.config, "Writing port %d to %s", port, m.config.PortFile)
	if err := os.WriteFile(m.config.PortFile, []byte(fmt.Sprintf("%d", port)), 0644); err != nil {
		debugLog(m.config, "ERROR: Failed to write port file: %v", err)
	}

	// Notify port monitor via named pipe
	m.notifyPortChange(port)

	// Send webhook notification (async)
	go m.sendWebhook(port)

	// Display success
	grn := colorGreen
	bold := colorBold
	nc := colorReset
	showSuccess(fmt.Sprintf("Port: %s%s%d%s", grn, bold, port, nc))

	// Show update tactics
	portAPIEnabled := os.Getenv("PORT_API_ENABLED") == "true"
	if portAPIEnabled {
		portAPIType := os.Getenv("PORT_API_TYPE")
		showSuccess(fmt.Sprintf("Updated via: File + API (%s)", portAPIType))
	} else {
		showSuccess("Updated via: File")
	}

	// Display expiration info
	secondsUntilExpiry := int(time.Until(expiresAt).Seconds())
	daysUntilExpiry := secondsUntilExpiry / 86400
	expiryDate := expiresAt.Format("2006-01-02 15:04:05")

	debugLog(m.config, "Expiration info: %d seconds (%d days)", secondsUntilExpiry, daysUntilExpiry)
	showSuccess(fmt.Sprintf("Port expires: %s (in %d days)", expiryDate, daysUntilExpiry))
	showSuccess(fmt.Sprintf("Keep-alive: Bind refresh every %d minutes", int(m.config.BindInterval.Minutes())))

	if m.config.SignatureRefreshDays > 0 {
		showSuccess(fmt.Sprintf("Signature refresh: Every %d days", m.config.SignatureRefreshDays))
	} else {
		debugLog(m.config, "Signature refresh disabled (SIGNATURE_REFRESH_DAYS=0, testing mode)")
	}

	showVPNConnected()

	debugLog(m.config, "Main loop initialized:")
	debugLog(m.config, "  BIND_COUNT=0")
	debugLog(m.config, "  SIGNATURE_REFRESH_COUNT=0")
	debugLog(m.config, "  LAST_SIGNATURE_TIME=%s", m.state.LastSignatureTime.Format("2006-01-02 15:04:05"))

	return nil
}

func (m *KeepaliveManager) keepaliveLoop(ctx context.Context) error {
	ticker := time.NewTicker(m.config.BindInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			debugLog(m.config, "Keepalive loop received shutdown signal")
			return nil

		case <-ticker.C:
			m.mu.Lock()
			currentTime := time.Now()
			timeSinceSignature := currentTime.Sub(m.state.LastSignatureTime)
			secondsUntilExpiry := int(time.Until(m.state.ExpiresAt).Seconds())

			debugLog(m.config, "====== Keep-alive cycle #%d ======", m.state.BindCount+1)
			debugLog(m.config, "Current time: %s", currentTime.Format("2006-01-02 15:04:05"))
			debugLog(m.config, "Time since last signature: %v (%d days)", timeSinceSignature, int(timeSinceSignature.Hours()/24))
			debugLog(m.config, "Seconds until expiry: %d (%d days)", secondsUntilExpiry, secondsUntilExpiry/86400)

			// Check if we need a new signature
			needNewSignature := false
			reason := ""

			// Reason 1: Scheduled refresh
			if timeSinceSignature >= time.Duration(m.config.SignatureRefreshDays)*24*time.Hour {
				needNewSignature = true
				reason = fmt.Sprintf("scheduled refresh (%d-day interval)", m.config.SignatureRefreshDays)
				debugLog(m.config, "Signature refresh trigger: %s", reason)
			}

			// Reason 2: Signature expiring soon
			safetyThreshold := time.Duration(m.config.SignatureSafetyHours) * time.Hour
			if time.Until(m.state.ExpiresAt) <= safetyThreshold {
				needNewSignature = true
				reason = fmt.Sprintf("signature expiring soon (within %dh)", m.config.SignatureSafetyHours)
				debugLog(m.config, "Signature refresh trigger: %s", reason)
			}

			if needNewSignature {
				m.state.SignatureRefreshCount++
				debugLog(m.config, "Initiating signature refresh #%d (reason: %s)", m.state.SignatureRefreshCount, reason)

				// Only show refresh message if not in rapid test mode
				// if m.config.SignatureRefreshDays > 0 {
				// 	blu := colorBlue
				// 	nc := colorReset
				// 	fmt.Printf("  %s↻%s [%s] Getting new signature (%s)...\n",
				// 		time.Now().Format("2006-01-02 15:04:05"), blu, nc, reason)
				// }

				m.mu.Unlock()
				if err := m.refreshSignature(ctx); err != nil {
					// Check if we're shutting down - if so, just return without triggering reconnection
					if ctx.Err() != nil {
						debugLog(m.config, "Context cancelled during signature refresh, exiting gracefully")
						return nil
					}

					m.mu.Lock()
					m.handleRefreshFailure(err)
					m.mu.Unlock()
					// handleRefreshFailure exits, so this line is never reached
					continue
				}

				// Reset ticker to start next cycle immediately with new signature
				ticker.Reset(m.config.BindInterval)
				debugLog(m.config, "Ticker reset after scheduled signature refresh")
				m.mu.Lock()
			} else {
				// Regular keep-alive bind
				m.state.BindCount++
				debugLog(m.config, "Performing regular keep-alive bind #%d...", m.state.BindCount)

				payload := m.state.Payload
				signature := m.state.Signature
				m.mu.Unlock()

				if err := m.client.BindPortWithRetry(ctx, payload, signature, 3*time.Minute); err != nil {
					// Check if we're shutting down
					if ctx.Err() != nil {
						debugLog(m.config, "Context cancelled during bind, exiting gracefully")
						return nil
					}

					// Check if this is an API error (status != OK)
					if _, ok := err.(*APIError); ok {
						// API error - signature/payload is invalid, get new signature immediately
						debugLog(m.config, "API error detected, escalating to signature refresh immediately")

						// Immediately refresh signature (don't wait for next cycle)
						if err := m.refreshSignature(ctx); err != nil {
							if ctx.Err() != nil {
								debugLog(m.config, "Context cancelled during signature refresh, exiting gracefully")
								return nil
							}
							m.mu.Lock()
							m.handleRefreshFailure(err)
							m.mu.Unlock()
							// handleRefreshFailure exits, unreachable
						}

						// Reset ticker to start next cycle immediately with new signature
						ticker.Reset(m.config.BindInterval)
						debugLog(m.config, "Ticker reset after API error escalation")

						// Don't continue - fall through to end of cycle to properly release lock and wait for next tick
						m.mu.Lock()
						debugLog(m.config, "Signature refresh complete, waiting for next cycle")
					} else {
						// Network error after 3 minutes - escalate to signature refresh
						debugLog(m.config, "Network error during bind after 3 minutes, escalating to signature refresh")

						// Try to get new signature
						if err := m.refreshSignature(ctx); err != nil {
							if ctx.Err() != nil {
								debugLog(m.config, "Context cancelled during signature refresh, exiting gracefully")
								return nil
							}
							m.mu.Lock()
							m.handleRefreshFailure(err)
							m.mu.Unlock()
							// handleRefreshFailure exits, unreachable
						}

						// Reset ticker to start next cycle immediately with new signature
						ticker.Reset(m.config.BindInterval)
						debugLog(m.config, "Ticker reset after network error escalation")

						// Don't continue - fall through to end of cycle to properly release lock and wait for next tick
						m.mu.Lock()
						debugLog(m.config, "Signature refresh complete, waiting for next cycle")
					}
				} else {
					m.mu.Lock()
					debugLog(m.config, "Keep-alive bind #%d successful", m.state.BindCount)
				}
			}

			debugLog(m.config, "====== End of cycle #%d ======", m.state.BindCount+m.state.SignatureRefreshCount)
			m.mu.Unlock()
		}
	}
}

func (m *KeepaliveManager) refreshSignature(ctx context.Context) error {
	resp, err := m.client.GetSignatureWithRetry(ctx, 2*time.Minute)
	if err != nil {
		// Check if cancelled - don't show error if shutting down
		if ctx.Err() != nil {
			debugLog(m.config, "Signature refresh cancelled due to context cancellation")
			return err
		}

		debugLog(m.config, "Signature refresh failed after 2 minutes")
		return err
	}

	debugLog(m.config, "New signature acquired successfully")

	// Parse new response
	newPort, newExpiresAt, err := ParsePayload(resp.Payload)
	if err != nil {
		showError(fmt.Sprintf("Failed to parse new signature: %v", err))
		debugLog(m.config, "New payload parsing failed: %v", err)
		return err
	}

	debugLog(m.config, "New signature values:")
	debugLog(m.config, "  Port: %d", newPort)
	debugLog(m.config, "  Payload length: %d bytes", len(resp.Payload))
	debugLog(m.config, "  Signature length: %d bytes", len(resp.Signature))
	debugLog(m.config, "  Expires at: %d (%s)", newExpiresAt.Unix(), newExpiresAt.Format("2006-01-02 15:04:05"))

	//showInfo()
	//showStep(fmt.Sprintf("New signature acquired with port: %d", newPort))

	// Check if port changed
	m.mu.Lock()
	oldPort := m.state.Port
	portChanged := newPort != oldPort
	m.mu.Unlock()

	if portChanged {
		debugLog(m.config, "Port changed: %d -> %d", oldPort, newPort)

		currentTime := time.Now()
		blu := colorBlue
		grn := colorGreen
		nc := colorReset
		fmt.Printf("\n  %s↻%s [%s] New Signature with Port: %s%d%s\n", blu, nc, currentTime.Format("2006-01-02 15:04:05"), grn, newPort, nc)

		// Write new port to file
		debugLog(m.config, "Writing new port to %s", m.config.PortFile)
		os.WriteFile(m.config.PortFile, []byte(fmt.Sprintf("%d", newPort)), 0644)
		m.notifyPortChange(newPort)

		// Notify webhook (async)
		go m.sendWebhook(newPort)
	} else {
		debugLog(m.config, "Port unchanged: %d", newPort)
	}

	// Update state
	m.mu.Lock()
	m.state.Port = newPort
	m.state.Payload = resp.Payload
	m.state.Signature = resp.Signature
	m.state.ExpiresAt = newExpiresAt
	m.state.LastSignatureTime = time.Now()

	debugLog(m.config, "Signature data updated")
	debugLog(m.config, "  LAST_SIGNATURE_TIME=%s", m.state.LastSignatureTime.Format("2006-01-02 15:04:05"))

	payload := m.state.Payload
	signature := m.state.Signature
	m.mu.Unlock()

	// Bind with new signature (3 minutes)
	debugLog(m.config, "Binding with new signature...")
	if err := m.client.BindPortWithRetry(ctx, payload, signature, 3*time.Minute); err != nil {
		// Check if cancelled
		if ctx.Err() != nil {
			debugLog(m.config, "Bind cancelled due to context cancellation")
			return err
		}

		showWarning("Got new signature but bind failed")
		debugLog(m.config, "Bind failed after fresh signature (unusual), will retry next cycle")
	} else {
		debugLog(m.config, "Bind with new signature successful")
		// if m.config.SignatureRefreshDays > 0 {
		// 	grn := colorGreen
		// 	nc := colorReset
		// 	expiryDate := newExpiresAt.Format("2006-01-02 15:04:05")
		// 	showSuccess(fmt.Sprintf("Signature refreshed, port %s%d%s expires: %s", grn, newPort, nc, expiryDate))
		// }
	}

	return nil
}

func (m *KeepaliveManager) handleRefreshFailure(err error) {
	showInfo()
	showError(fmt.Sprintf("Signature refresh failed: %v", err))
	showError("Reconnecting...")
	debugLog(m.config, "Signature refresh failed, triggering VPN reconnection")

	// Immediate reconnect on any signature refresh failure
	os.WriteFile("/tmp/pf_signature_failed", []byte(""), 0644)
	os.Exit(1)
}

func (m *KeepaliveManager) notifyPortChange(port int) {
	pipePath := "/tmp/port_change_pipe"

	// Non-blocking write to pipe
	go func() {
		if _, err := os.Stat(pipePath); err == nil {
			// Pipe exists, try to write (with timeout)
			if file, err := os.OpenFile(pipePath, os.O_WRONLY|syscall.O_NONBLOCK, 0644); err == nil {
				defer file.Close()
				file.WriteString(fmt.Sprintf("%d\n", port))
				debugLog(m.config, "Notified port monitor of new port: %d", port)
			} else {
				debugLog(m.config, "Failed to open port change pipe: %v", err)
			}
		} else {
			debugLog(m.config, "Port change pipe not found, monitor may not be running yet")
		}
	}()
}

func (m *KeepaliveManager) sendWebhook(port int) {
	if m.config.WebhookURL == "" {
		debugLog(m.config, "No WEBHOOK_URL configured, skipping notification")
		return
	}

	debugLog(m.config, "Sending webhook notification for port %d...", port)

	// Get VPN IP (with timeout)
	vpnIP := ""
	client := &http.Client{Timeout: 5 * time.Second}
	if resp, err := client.Get("https://api.ipify.org"); err == nil {
		defer resp.Body.Close()
		if body, err := os.ReadFile(""); err == nil {
			vpnIP = string(body)
		}
	}
	debugLog(m.config, "Public VPN IP: %s", vpnIP)

	// Prepare JSON payload
	payload := map[string]interface{}{
		"port":      port,
		"timestamp": time.Now().Format(time.RFC3339),
	}
	if vpnIP != "" {
		payload["ip"] = vpnIP
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		debugLog(m.config, "Failed to marshal webhook payload: %v", err)
		return
	}

	debugLog(m.config, "Webhook payload: %s", string(jsonData))
	debugLog(m.config, "Executing webhook POST to %s", m.config.WebhookURL)

	// Send webhook (don't block on failure)
	// TODO: Implement actual webhook POST if needed
	debugLog(m.config, "Webhook notification sent successfully")
}
