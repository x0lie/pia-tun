package portforward

import (
	"context"
	"fmt"
	"time"

	"github.com/x0lie/pia-tun/internal/firewall"
	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/metrics"
	"github.com/x0lie/pia-tun/internal/portsync"
)

// Config holds port forwarding configuration.
type Config struct {
	Enabled              bool
	BindInterval         time.Duration
	SignatureRefreshDays int
	SignatureSafetyHours int
	PortFile             string
}

// ConnectionConfig holds the VPN connection details needed for port forwarding.
type ConnectionConfig struct {
	Token     string // PIA authentication token
	ClientIP  string // Client's tunnel IP
	ServerCN  string // Server certificate name
	PFGateway string // Port forwarding gateway IP
}

// Run starts the port forwarding service. This is the main entry point called by the orchestrator.
// connCfg provides the VPN connection details needed for port forwarding.
// onReconnect is called when a reconnect is needed (e.g., port change or API failure).
func Run(ctx context.Context, cfg *Config, connCfg *ConnectionConfig, onReconnect func(), metrics *metrics.Metrics, syncer *portsync.Syncer, fw *firewall.Firewall) error {
	if connCfg.PFGateway == "" {
		log.Error("Port forwarding unavailable: no PF gateway")
		<-ctx.Done()
		return nil
	}

	logger := log.New("portforward")

	logger.Debug("Port forwarding configuration:")
	logger.Debug("  BIND_INTERVAL=%v (%dmin)", cfg.BindInterval, int(cfg.BindInterval.Minutes()))
	logger.Debug("  SIGNATURE_REFRESH_DAYS=%d", cfg.SignatureRefreshDays)
	logger.Debug("  SIGNATURE_SAFETY_HOURS=%d", cfg.SignatureSafetyHours)
	logger.Debug("  PF_GATEWAY=%s", connCfg.PFGateway)
	logger.Debug("  TOKEN length: %d", len(connCfg.Token))
	logger.Debug("  PEER_IP: %s", connCfg.ClientIP)
	logger.Debug("  PIA_CN: %s", connCfg.ServerCN)

	client := NewPIAClient(cfg, connCfg, logger)
	manager := NewKeepaliveManager(cfg, client, logger, onReconnect, metrics, syncer, fw)

	if err := manager.Run(ctx); err != nil {
		return fmt.Errorf("port forwarding failed: %w", err)
	}

	return nil
}
