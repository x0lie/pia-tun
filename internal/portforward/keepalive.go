package portforward

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/x0lie/pia-tun/internal/firewall"
	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/metrics"
	"github.com/x0lie/pia-tun/internal/portsync"
)

// KeepaliveManager manages port forward state lifecycle.
type KeepaliveManager struct {
	config      *Config
	client      *PIAClient
	log         *log.Logger
	state       *State
	mu          sync.Mutex
	onReconnect func()
	metrics     *metrics.Metrics
	syncer      *portsync.Syncer
	fw          *firewall.Firewall
}

// State tracks port forwarding state.
type State struct {
	Port                  int
	Payload               string
	Signature             string
	ExpiresAt             time.Time
	LastSignatureTime     time.Time
	BindCount             int
	SignatureRefreshCount int
}

// NewKeepaliveManager creates a new keepalive manager.
func NewKeepaliveManager(config *Config, client *PIAClient, logger *log.Logger, onReconnect func(), metrics *metrics.Metrics, syncer *portsync.Syncer, fw *firewall.Firewall) *KeepaliveManager {
	return &KeepaliveManager{
		config:      config,
		client:      client,
		log:         logger,
		state:       &State{},
		onReconnect: onReconnect,
		metrics:     metrics,
		syncer:      syncer,
		fw:          fw,
	}
}

// Run performs initial setup then enters the keepalive loop.
func (m *KeepaliveManager) Run(ctx context.Context) error {
	if err := m.initialSetup(); err != nil {
		return err
	}
	m.log.Debug("Port forwarding setup complete, entering main loop")

	return m.keepaliveLoop(ctx)
}

func (m *KeepaliveManager) initialSetup() error {
	log.Step("Acquiring port forward signature...")
	m.log.Debug("Starting initial signature acquisition (max retries: 5)")

	ctx := context.Background()

	resp, err := m.client.GetSignatureWithRetry(ctx, 5*time.Minute)
	if err != nil {
		log.Error(fmt.Sprintf("Port forwarding failed: %v", err))
		m.log.Debug("Initial signature acquisition failed, triggering reconnect")
		m.triggerReconnect()
		return fmt.Errorf("signature acquisition failed: %w", err)
	}

	m.log.Debug("Parsing initial signature response...")
	port, expiresAt, err := ParsePayload(resp.Payload)
	if err != nil {
		log.Error(fmt.Sprintf("Failed to parse signature: %v", err))
		m.log.Debug("Payload parsing failed: %v", err)
		m.triggerReconnect()
		return fmt.Errorf("payload parsing failed: %w", err)
	}

	m.log.Debug("Initial parsed values:")
	m.log.Debug("  Port: %d", port)
	m.log.Debug("  Payload length: %d bytes", len(resp.Payload))
	m.log.Debug("  Signature length: %d bytes", len(resp.Signature))
	m.log.Debug("  Expires at: %d (%s)", expiresAt.Unix(), expiresAt.Format("2006-01-02 15:04:05"))

	if port == 0 {
		log.Error("Failed to extract port from response")
		m.log.Debug("PORT is zero after parsing")
		m.triggerReconnect()
		return fmt.Errorf("port is zero after parsing")
	}

	m.mu.Lock()
	m.state.Port = port
	m.state.Payload = resp.Payload
	m.state.Signature = resp.Signature
	m.state.ExpiresAt = expiresAt
	m.state.LastSignatureTime = time.Now()
	m.mu.Unlock()

	m.log.Debug("Performing initial bind...")
	if err := m.client.BindPortWithRetry(ctx, resp.Payload, resp.Signature, 3*time.Minute); err != nil {
		log.Warning("Initial bind failed, but continuing...")
		m.log.Debug("Initial bind failure is non-fatal, continuing with port announcement")
	} else {
		m.log.Debug("Initial bind successful")
	}

	m.log.Debug("Writing port %d to %s", port, m.config.PortFile)
	if err := os.WriteFile(m.config.PortFile, []byte(fmt.Sprintf("%d", port)), 0644); err != nil {
		log.Error(fmt.Sprintf("ERROR: Failed to write port file: %v", err))
	}

	m.metrics.UpdatePortForwarding(true, port)
	if m.syncer != nil {
		m.syncer.NotifyPort(port)
	}
	if m.fw != nil {
		if err := m.fw.AllowForwardedPort(port); err != nil {
			log.Warning(fmt.Sprintf("Failed to add firewall rule for port %d: %v", port, err))
		}
	}

	log.Success(fmt.Sprintf("Port: %s%s%d%s", log.ColorGreen, log.ColorBold, port, log.ColorReset))

	secondsUntilExpiry := int(time.Until(expiresAt).Seconds())
	daysUntilExpiry := secondsUntilExpiry / 86400
	expiryDate := expiresAt.Format("2006-01-02 15:04:05")

	m.log.Debug("Expiration info: %d seconds (%d days)", secondsUntilExpiry, daysUntilExpiry)
	log.Success(fmt.Sprintf("Expires: %s (%d days)", expiryDate, daysUntilExpiry))
	log.Success(fmt.Sprintf("Keep-alive: Bind refresh every %d minutes", int(m.config.BindInterval.Minutes())))

	if m.config.SignatureRefreshDays > 0 {
		log.Success(fmt.Sprintf("Signature refresh: Every %d days", m.config.SignatureRefreshDays))
	} else {
		m.log.Debug("Signature refresh disabled (SIGNATURE_REFRESH_DAYS=0, testing mode)")
	}

	m.log.Debug("Main loop initialized:")
	m.log.Debug("  BIND_COUNT=0")
	m.log.Debug("  SIGNATURE_REFRESH_COUNT=0")
	m.log.Debug("  LAST_SIGNATURE_TIME=%s", m.state.LastSignatureTime.Format("2006-01-02 15:04:05"))

	return nil
}

func (m *KeepaliveManager) keepaliveLoop(ctx context.Context) error {
	ticker := time.NewTicker(m.config.BindInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.metrics.UpdatePortForwarding(false, 0)
			if m.fw != nil {
				m.fw.RemoveForwardedPort()
			}
			m.log.Debug("Keepalive loop received shutdown signal")
			return nil

		case <-ticker.C:
			m.mu.Lock()
			currentTime := time.Now()
			timeSinceSignature := currentTime.Sub(m.state.LastSignatureTime)
			secondsUntilExpiry := int(time.Until(m.state.ExpiresAt).Seconds())

			m.log.Debug("====== Keep-alive cycle #%d ======", m.state.BindCount+1)
			m.log.Debug("Current time: %s", currentTime.Format("2006-01-02 15:04:05"))
			m.log.Debug("Time since last signature: %v (%d days)", timeSinceSignature, int(timeSinceSignature.Hours()/24))
			m.log.Debug("Seconds until expiry: %d (%d days)", secondsUntilExpiry, secondsUntilExpiry/86400)

			needNewSignature := false
			reason := ""

			if timeSinceSignature >= time.Duration(m.config.SignatureRefreshDays)*24*time.Hour {
				needNewSignature = true
				reason = fmt.Sprintf("scheduled refresh (%d-day interval)", m.config.SignatureRefreshDays)
				m.log.Debug("Signature refresh trigger: %s", reason)
			}

			safetyThreshold := time.Duration(m.config.SignatureSafetyHours) * time.Hour
			if time.Until(m.state.ExpiresAt) <= safetyThreshold {
				needNewSignature = true
				reason = fmt.Sprintf("signature expiring soon (within %dh)", m.config.SignatureSafetyHours)
				m.log.Debug("Signature refresh trigger: %s", reason)
			}

			if needNewSignature {
				m.state.SignatureRefreshCount++
				m.log.Debug("Initiating signature refresh #%d (reason: %s)", m.state.SignatureRefreshCount, reason)

				m.mu.Unlock()
				if err := m.refreshSignature(ctx); err != nil {
					if ctx.Err() != nil {
						m.log.Debug("Context cancelled during signature refresh, exiting gracefully")
						return nil
					}

					m.mu.Lock()
					m.handleRefreshFailure(err)
					m.mu.Unlock()
					continue
				}

				ticker.Reset(m.config.BindInterval)
				m.log.Debug("Ticker reset after scheduled signature refresh")
				m.mu.Lock()
			} else {
				m.state.BindCount++
				m.log.Debug("Performing regular keep-alive bind #%d...", m.state.BindCount)

				payload := m.state.Payload
				signature := m.state.Signature
				m.mu.Unlock()

				if err := m.client.BindPortWithRetry(ctx, payload, signature, 3*time.Minute); err != nil {
					if ctx.Err() != nil {
						m.log.Debug("Context cancelled during bind, exiting gracefully")
						return nil
					}

					if _, ok := err.(*APIError); ok {
						m.log.Debug("API error detected, escalating to signature refresh immediately")

						if err := m.refreshSignature(ctx); err != nil {
							if ctx.Err() != nil {
								m.log.Debug("Context cancelled during signature refresh, exiting gracefully")
								return nil
							}
							m.mu.Lock()
							m.handleRefreshFailure(err)
							m.mu.Unlock()
						}

						ticker.Reset(m.config.BindInterval)
						m.log.Debug("Ticker reset after API error escalation")

						m.mu.Lock()
						m.log.Debug("Signature refresh complete, waiting for next cycle")
					} else {
						m.log.Debug("Network error during bind after 3 minutes, escalating to signature refresh")

						if err := m.refreshSignature(ctx); err != nil {
							if ctx.Err() != nil {
								m.log.Debug("Context cancelled during signature refresh, exiting gracefully")
								return nil
							}
							m.mu.Lock()
							m.handleRefreshFailure(err)
							m.mu.Unlock()
						}

						ticker.Reset(m.config.BindInterval)
						m.log.Debug("Ticker reset after network error escalation")

						m.mu.Lock()
						m.log.Debug("Signature refresh complete, waiting for next cycle")
					}
				} else {
					m.mu.Lock()
					m.log.Debug("Keep-alive bind #%d successful", m.state.BindCount)
				}
			}

			m.log.Debug("====== End of cycle #%d ======", m.state.BindCount+m.state.SignatureRefreshCount)
			m.mu.Unlock()
		}
	}
}

func (m *KeepaliveManager) refreshSignature(ctx context.Context) error {
	resp, err := m.client.GetSignatureWithRetry(ctx, 2*time.Minute)
	if err != nil {
		if ctx.Err() != nil {
			m.log.Debug("Signature refresh cancelled due to context cancellation")
			return err
		}

		m.log.Debug("Signature refresh failed after 2 minutes")
		return err
	}

	m.log.Debug("New signature acquired successfully")

	newPort, newExpiresAt, err := ParsePayload(resp.Payload)
	if err != nil {
		log.Error(fmt.Sprintf("Failed to parse new signature: %v", err))
		m.log.Debug("New payload parsing failed: %v", err)
		return err
	}

	m.log.Debug("New signature values:")
	m.log.Debug("  Port: %d", newPort)
	m.log.Debug("  Payload length: %d bytes", len(resp.Payload))
	m.log.Debug("  Signature length: %d bytes", len(resp.Signature))
	m.log.Debug("  Expires at: %d (%s)", newExpiresAt.Unix(), newExpiresAt.Format("2006-01-02 15:04:05"))

	m.mu.Lock()
	oldPort := m.state.Port
	portChanged := newPort != oldPort
	m.mu.Unlock()

	if portChanged {
		m.log.Debug("Port changed: %d -> %d", oldPort, newPort)

		currentTime := time.Now()
		fmt.Printf("\n  %s\u21bb%s [%s] New Signature with Port: %s%d%s\n", log.ColorBlue, log.ColorReset, currentTime.Format("2006-01-02 15:04:05"), log.ColorGreen, newPort, log.ColorReset)

		m.log.Debug("Writing new port to %s", m.config.PortFile)
		os.WriteFile(m.config.PortFile, []byte(fmt.Sprintf("%d", newPort)), 0644)
		m.metrics.UpdatePortForwarding(true, newPort)
		if m.syncer != nil {
			m.syncer.NotifyPort(newPort)
		}
		if m.fw != nil {
			if err := m.fw.AllowForwardedPort(newPort); err != nil {
				log.Warning(fmt.Sprintf("Failed to update firewall rule for port %d: %v", newPort, err))
			}
		}
	} else {
		m.log.Debug("Port unchanged: %d", newPort)
	}

	m.mu.Lock()
	m.state.Port = newPort
	m.state.Payload = resp.Payload
	m.state.Signature = resp.Signature
	m.state.ExpiresAt = newExpiresAt
	m.state.LastSignatureTime = time.Now()

	m.log.Debug("Signature data updated")
	m.log.Debug("  LAST_SIGNATURE_TIME=%s", m.state.LastSignatureTime.Format("2006-01-02 15:04:05"))

	payload := m.state.Payload
	signature := m.state.Signature
	m.mu.Unlock()

	m.log.Debug("Binding with new signature...")
	if err := m.client.BindPortWithRetry(ctx, payload, signature, 3*time.Minute); err != nil {
		if ctx.Err() != nil {
			m.log.Debug("Bind cancelled due to context cancellation")
			return err
		}

		log.Warning("Got new signature but bind failed")
		m.log.Debug("Bind failed after fresh signature (unusual), will retry next cycle")
	} else {
		m.log.Debug("Bind with new signature successful")
	}

	return nil
}

func (m *KeepaliveManager) handleRefreshFailure(err error) {
	log.Blank()
	log.Error(fmt.Sprintf("Signature refresh failed: %v", err))
	log.Error("Reconnecting...")
	m.log.Debug("Signature refresh failed, triggering VPN reconnection")

	m.triggerReconnect()
}

// triggerReconnect signals that a VPN reconnection is needed.
// In orchestrated mode, calls the onReconnect callback.
// In legacy standalone mode, exits immediately.
func (m *KeepaliveManager) triggerReconnect() {
	if m.onReconnect != nil {
		m.log.Debug("Signaling orchestrator to reconnect")
		m.onReconnect()
		return
	}

	// Legacy standalone mode
	os.Exit(1)
}
