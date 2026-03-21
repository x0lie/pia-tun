package portforward

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/x0lie/pia-tun/internal/apperrors"
	"github.com/x0lie/pia-tun/internal/cacher"
	"github.com/x0lie/pia-tun/internal/firewall"
	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/metrics"
	"github.com/x0lie/pia-tun/internal/pia"
	"github.com/x0lie/pia-tun/internal/portsync"
)

type Config struct {
	Enabled              bool
	BindInterval         time.Duration
	SignatureSafetyHours int
	PortFile             string
}

type ConnectionConfig struct {
	ClientIP  string
	ServerCN  string
	PFGateway string
}

type manager struct {
	cfg        *Config
	connCfg    *ConnectionConfig
	cache      *cacher.Cache
	log        *log.Logger
	httpClient *http.Client
	state      *state
	metrics    *metrics.Metrics
	syncer     *portsync.Syncer
	fw         *firewall.Firewall
}

type state struct {
	port      int
	payload   string
	signature string
	expiresAt time.Time
	bindTime  time.Time
}

const (
	pfAPIPort        = 19999
	retryInterval    = 15 * time.Second
	portBindDuration = 20 * time.Minute // conservative evaluation of port bind death (tested ~23 minute lifespan)
)

func newManager(config *Config, connConfig *ConnectionConfig, cache *cacher.Cache, logger *log.Logger, metrics *metrics.Metrics, syncer *portsync.Syncer, fw *firewall.Firewall) *manager {
	return &manager{
		cfg:        config,
		connCfg:    connConfig,
		cache:      cache,
		log:        logger,
		httpClient: pia.NewBoundClient(3*time.Second, 3*time.Second),
		state:      &state{},
		metrics:    metrics,
		syncer:     syncer,
		fw:         fw,
	}
}

func Run(ctx context.Context, cfg *Config, connCfg *ConnectionConfig, cache *cacher.Cache, metrics *metrics.Metrics, syncer *portsync.Syncer, fw *firewall.Firewall) error {
	if connCfg.PFGateway == "" {
		return fmt.Errorf("port forwarding unavailable: no pf gateway")
	}
	if err := fw.AddPFRoute(connCfg.PFGateway); err != nil {
		return fmt.Errorf("failed to add pf gateway route: %v", err)
	}

	logger := log.New("portforward")

	logger.Trace("Port forwarding configuration:")
	logger.Trace("  BIND_INTERVAL=%v", cfg.BindInterval)
	logger.Trace("  SIGNATURE_SAFETY_HOURS=%d", cfg.SignatureSafetyHours)
	logger.Trace("  PF_GATEWAY=%s", connCfg.PFGateway)
	logger.Trace("  TOKEN length: %d", len(cache.GetToken()))
	logger.Trace("  PEER_IP: %s", connCfg.ClientIP)
	logger.Trace("  PIA_CN: %s", connCfg.ServerCN)

	m := newManager(cfg, connCfg, cache, logger, metrics, syncer, fw)

	m.state.bindTime = time.Now().Add(-portBindDuration + time.Minute) // To limit initial run's failure threshold to 1 minute

	log.Step("Acquiring forwarded port...")
	if err := m.acquirePort(ctx); err != nil {
		m.teardown()
		return fmt.Errorf("%w: %w", err, apperrors.ErrReconnect)
	}
	m.announcePort()

	if m.cfg.BindInterval != 900*time.Second {
		log.Success("Keep-alive: Bind refresh every %d minutes", int(m.cfg.BindInterval.Minutes()))
	}
	if m.cfg.SignatureSafetyHours != 6 {
		log.Success("Signature safety hours: %d hours", m.cfg.SignatureSafetyHours)
	}

	m.log.Debug("Port forwarding setup complete, entering keepalive loop")

	ticker := time.NewTicker(m.cfg.BindInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.log.Debug("Received shutdown signal")
			m.teardown()
			return nil
		case <-ticker.C:
			if time.Duration(m.cfg.SignatureSafetyHours)*time.Hour > time.Until(m.state.expiresAt) {
				log.Step("Renewing forwarded port...")
				if err := m.acquirePort(ctx); err != nil {
					m.teardown()
					return fmt.Errorf("%w: %w", err, apperrors.ErrReconnect)
				}
				m.announcePort()
				continue
			}
			if err := m.bindPortWithRetry(ctx, m.state.payload, m.state.signature); err != nil {
				m.teardown()
				return fmt.Errorf("%w: %w", err, apperrors.ErrReconnect)
			}
		}
	}
}

func (m *manager) acquirePort(ctx context.Context) error {
	resp, err := m.getSignatureWithRetry(ctx)
	if err != nil {
		return err
	}

	m.log.Trace("Parsing initial signature response...")
	port, expiresAt, err := parsePayload(resp.Payload)
	if err != nil {
		return err
	}

	m.log.Trace("Initial parsed values:")
	m.log.Trace("  Port: %d", port)
	m.log.Trace("  Payload length: %d bytes", len(resp.Payload))
	m.log.Trace("  Signature length: %d bytes", len(resp.Signature))
	m.log.Trace("  Expires at: %d (%s)", expiresAt.Unix(), expiresAt.Format("2006-01-02 15:04:05"))

	if port == 0 {
		return fmt.Errorf("port is zero after parsing")
	}

	m.state.port = port
	m.state.payload = resp.Payload
	m.state.signature = resp.Signature
	m.state.expiresAt = expiresAt

	m.log.Debug("Performing initial bind...")
	if err := m.bindPortWithRetry(ctx, resp.Payload, resp.Signature); err != nil {
		return err
	}
	m.log.Debug("Initial bind successful")

	return nil
}

func (m *manager) announcePort() {
	log.Success("Port: %s%s%d%s", log.ColorGreen, log.ColorBold, m.state.port, log.ColorReset)

	// Calculate and log renewal/expiry
	renewalTime := m.state.expiresAt.Add(-time.Duration(m.cfg.SignatureSafetyHours) * time.Hour)
	daysUntilRenewal := int(time.Until(renewalTime).Hours()) / 24
	renewalDate := renewalTime.Format("2006-01-02")
	m.log.Debug("Expires: %s", m.state.expiresAt)
	log.Success("Renews: %s (%d days)", renewalDate, daysUntilRenewal)

	// Allow port through firewall
	if err := m.fw.AllowForwardedPort(m.state.port); err != nil {
		log.Warning("Failed to add firewall rule for port %d: %v", m.state.port, err)
	}

	// Write Port to file
	m.log.Debug("Writing port %d to %s", m.state.port, m.cfg.PortFile)
	if err := os.WriteFile(m.cfg.PortFile, []byte(fmt.Sprintf("%d", m.state.port)), 0644); err != nil {
		log.Error("failed to write port file: %v", err)
	}

	// Update metric
	m.metrics.UpdatePortForwarding(true, m.state.port)

	// Pass to portSyncer
	if m.syncer != nil {
		m.syncer.NotifyPort(m.state.port)
	}
}

func (m *manager) teardown() {
	m.fw.RemovePFRoute(m.connCfg.PFGateway)
	m.fw.RemoveForwardedPort()
	m.metrics.UpdatePortForwarding(false, 0)
}
