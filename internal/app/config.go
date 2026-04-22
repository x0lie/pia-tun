package app

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/metrics"
	"github.com/x0lie/pia-tun/internal/monitor"
	"github.com/x0lie/pia-tun/internal/portforward"
	"github.com/x0lie/pia-tun/internal/portsync"
	"github.com/x0lie/pia-tun/internal/proxy"
)

type Config struct {
	Version      string
	LogLevel     string
	SHA          string
	DNS          []string
	DNSMode      string
	BootstrapDNS []string

	PIA     PIA
	VPN     VPN
	FW      FW
	PF      portforward.Config
	PS      portsync.Config
	Proxy   proxy.Config
	Metrics metrics.Config
	Monitor monitor.Config
}

type PIA struct {
	User     string
	Pass     string
	Location string
	CN       string
	IP       string
}

type VPN struct {
	MTU         int
	IPv6Enabled bool
	Backend     string
}

type FW struct {
	Backend string
	LANs    string
}

func LoadConfig() Config {
	setupAutoEnable()
	dnsMode, dns := parseDNS(getEnv("DNS", "pia"))
	bootstrapDNS := parseBootstrapDNS(getEnv("BOOTSTRAP_DNS", ""))
	log.Level = parseLogLevel(getEnv("LOG_LEVEL", "info"))

	return Config{
		Version:      getEnv("VERSION", "local"),
		LogLevel:     getEnv("LOG_LEVEL", "info"),
		SHA:          getEnv("SHA", ""),
		DNS:          dns,
		DNSMode:      dnsMode,
		BootstrapDNS: bootstrapDNS,

		PIA:     loadPIAConfig(),
		VPN:     loadVPNConfig(),
		FW:      loadFWConfig(),
		PF:      loadPFConfig(),
		PS:      loadPSConfig(),
		Proxy:   loadProxyConfig(),
		Metrics: loadMetricsConfig(),
		Monitor: loadMonitorConfig(),
	}
}

func loadPIAConfig() PIA {
	return PIA{
		User:     getEnvOrSecret("PIA_USER", ""),
		Pass:     getEnvOrSecret("PIA_PASS", ""),
		Location: getEnv("PIA_LOCATIONS", "all"),
		CN:       getEnv("PIA_CN", ""),
		IP:       getEnv("PIA_IP", ""),
	}
}

func loadVPNConfig() VPN {
	return VPN{
		MTU:         getEnvInt("MTU", 1420),
		Backend:     getEnv("WG_BACKEND", ""),
		IPv6Enabled: getEnvBool("IPV6_ENABLED", false),
	}
}

func loadFWConfig() FW {
	return FW{
		Backend: getEnv("IPT_BACKEND", ""),
		LANs:    getEnv("LOCAL_NETWORKS", ""),
	}
}

func loadPFConfig() portforward.Config {
	return portforward.Config{
		Enabled:              getEnvBool("PF_ENABLED", false),
		BindInterval:         time.Duration(getEnvInt("PF_BIND_INTERVAL", 900)) * time.Second,
		SignatureSafetyHours: getEnvInt("PF_SIGNATURE_SAFETY_HOURS", 6),
		PortFile:             getEnv("PORT_FILE", "/run/pia-tun/port"),
	}
}

func loadPSConfig() portsync.Config {
	return portsync.Config{
		Client: getEnv("PS_CLIENT", ""),
		URL:    getEnv("PS_URL", ""),
		User:   getEnvOrSecret("PS_USER", ""),
		Pass:   getEnvOrSecret("PS_PASS", ""),
		Script: getEnv("PS_SCRIPT", ""),
	}
}

func loadProxyConfig() proxy.Config {
	return proxy.Config{
		HTTPEnabled:   getEnvBool("HTTP_PROXY_ENABLED", false),
		Socks5Enabled: getEnvBool("SOCKS5_ENABLED", false),
		User:          getEnvOrSecret("PROXY_USER", ""),
		Pass:          getEnvOrSecret("PROXY_PASS", ""),
		Socks5Port:    getEnvInt("SOCKS5_PORT", 1080),
		HTTPPort:      getEnvInt("HTTP_PROXY_PORT", 8888),
	}
}

func loadMetricsConfig() metrics.Config {
	return metrics.Config{
		Enabled: getEnvBool("METRICS_ENABLED", true),
		Port:    getEnvInt("METRICS_PORT", 9090),
		Name:    getEnv("INSTANCE_NAME", ""),
	}
}

func loadMonitorConfig() monitor.Config {
	return monitor.Config{
		Interval:      time.Duration(getEnvInt("HC_INTERVAL", 10)) * time.Second,
		FailureWindow: time.Duration(getEnvInt("HC_FAILURE_WINDOW", 30)) * time.Second,
	}
}

func (c *Config) validate() error {
	if c.PIA.User == "" || c.PIA.Pass == "" {
		return fmt.Errorf("Set PIA_USER and PIA_PASS environment variables, or use Docker secrets at /run/secrets/pia_user and pia_pass")
	}
	return nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// getEnvOrSecret checks env var first, then Docker secrets at /run/secrets/<key>.
// File contents are trimmed of whitespace.
func getEnvOrSecret(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	secretPath := "/run/secrets/" + strings.ToLower(key)
	data, err := os.ReadFile(secretPath)
	if err == nil {
		return strings.TrimSpace(string(data))
	}
	if !os.IsNotExist(err) {
		log.Warning("cannot read secret %s: %v - ensure file is owned by root with chmod 600", strings.ToLower(key), err)
	}

	return def
}

func getEnvBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		return v == "true"
	}
	return def
}

func getEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func parseLogLevel(level string) int {
	switch strings.ToLower(level) {
	case "trace", "3":
		return 3
	case "debug", "2":
		return 2
	case "error", "0":
		return 0
	default:
		return 1
	}
}

func parseDNS(dns string) (string, []string) {
	switch dns {
	case "pia":
		return "pia", nil
	case "system":
		return "system", nil
	default:
		var servers []string
		dot := strings.Contains(dns, "tls://")
		for _, s := range strings.Split(dns, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				servers = append(servers, s)
			}
		}
		if dot {
			return "dot", servers
		}
		return "do53", servers
	}
}

func parseBootstrapDNS(dns string) []string {
	if dns == "" {
		return nil
	}
	var servers []string
	for _, s := range strings.Split(dns, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			servers = append(servers, s)
		}
	}
	return servers
}

func setupAutoEnable() {
	if os.Getenv("PS_CLIENT") != "" || os.Getenv("PS_SCRIPT") != "" {
		os.Setenv("PF_ENABLED", "true")
	}
}

func (a *App) logConfig() {
	a.log.Debug("Environment configuration:")
	a.log.Debug("  PIA_LOCATIONS=%s", a.cfg.PIA.Location)
	a.log.Debug("  LOG_LEVEL=%s", a.cfg.LogLevel)
	a.log.Debug("  IPV6_ENABLED=%v", a.cfg.VPN.IPv6Enabled)
	a.log.Debug("  LOCAL_NETWORKS=%s", a.cfg.FW.LANs)
	a.log.Debug("  DNS=%v", strings.Join(a.cfg.DNS, ", "))
	a.log.Debug("  MTU=%d", a.cfg.VPN.MTU)
	a.log.Debug("  WG_BACKEND=%s", a.cfg.VPN.Backend)
	a.log.Debug("  IPT_BACKEND=%s", a.cfg.FW.Backend)
	a.log.Debug("  PF_ENABLED=%v", a.cfg.PF.Enabled)
	a.log.Debug("  PORT_FILE=%s", a.cfg.PF.PortFile)
	a.log.Debug("  PS_CLIENT=%s", a.cfg.PS.Client)
	a.log.Debug("  PS_URL=%s", a.cfg.PS.URL)
	a.log.Debug("  PS_SCRIPT=%s", a.cfg.PS.Script)
	a.log.Debug("  SOCKS5_ENABLED=%v", a.cfg.Proxy.Socks5Enabled)
	a.log.Debug("  SOCKS5_PORT=%d", a.cfg.Proxy.Socks5Port)
	a.log.Debug("  HTTP_PROXY_ENABLED=%v", a.cfg.Proxy.HTTPEnabled)
	a.log.Debug("  HTTP_PROXY_PORT=%v", a.cfg.Proxy.HTTPPort)
	a.log.Debug("  METRICS_ENABLED=%v", a.cfg.Metrics.Enabled)
	a.log.Debug("  METRICS_PORT=%d", a.cfg.Metrics.Port)
	a.log.Debug("  INSTANCE_NAME=%s", a.cfg.Metrics.Name)
	a.log.Debug("  HC_INTERVAL=%s", a.cfg.Monitor.Interval)
	a.log.Debug("  HC_FAILURE_WINDOW=%s", a.cfg.Monitor.FailureWindow)
}
