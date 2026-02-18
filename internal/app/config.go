package app

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	PIAUser     string
	PIAPass     string
	PIALocation string

	LogLevel string

	IPv6Enabled    bool
	PFEnabled      bool
	ProxyEnabled   bool
	MetricsEnabled bool

	LocalNetworks string
	DNS           string

	MTU int

	WGBackend  string
	IPTBackend string

	PortFile string

	PSClient string
	PSURL    string
	PSUser   string
	PSPass   string
	PSCmd    string

	ProxyUser     string
	ProxyPass     string
	Socks5Port    int
	HTTPProxyPort int

	PIACN string
	PIAIP string

	MetricsPort  int
	InstanceName string

	HealthCheckInterval time.Duration
	HealthFailureWindow time.Duration
}

func LoadConfig() Config {
	setupAutoEnable()
	exportLogLevelForBash(os.Getenv("LOG_LEVEL"))

	return Config{
		PIAUser:     getEnvOrSecret("PIA_USER", ""),
		PIAPass:     getEnvOrSecret("PIA_PASS", ""),
		PIALocation: getEnv("PIA_LOCATION", ""),

		LogLevel: getEnv("LOG_LEVEL", "info"),

		IPv6Enabled:    getEnvBool("IPV6_ENABLED", false),
		PFEnabled:      getEnvBool("PF_ENABLED", false),
		ProxyEnabled:   getEnvBool("PROXY_ENABLED", false),
		MetricsEnabled: getEnvBool("METRICS_ENABLED", true),

		LocalNetworks: getEnv("LOCAL_NETWORKS", ""),
		DNS:           getEnv("DNS", "pia"),

		MTU: getEnvInt("MTU", 1420),

		WGBackend:  getEnv("WG_BACKEND", ""),
		IPTBackend: getEnv("IPT_BACKEND", ""),

		PortFile: getEnv("PORT_FILE", "/run/pia-tun/port"),

		PSClient: getEnv("PS_CLIENT", ""),
		PSURL:    getEnv("PS_URL", ""),
		PSUser:   getEnvOrSecret("PS_USER", ""),
		PSPass:   getEnvOrSecret("PS_PASS", ""),
		PSCmd:    getEnv("PS_CMD", ""),

		ProxyUser:     getEnvOrSecret("PROXY_USER", ""),
		ProxyPass:     getEnvOrSecret("PROXY_PASS", ""),
		Socks5Port:    getEnvInt("SOCKS5_PORT", 1080),
		HTTPProxyPort: getEnvInt("HTTP_PROXY_PORT", 8888),

		PIACN: getEnv("PIA_CN", ""),
		PIAIP: getEnv("PIA_IP", ""),

		MetricsPort:  getEnvInt("METRICS_PORT", 9090),
		InstanceName: getEnv("INSTANCE_NAME", ""),

		HealthCheckInterval: time.Duration(getEnvInt("HC_INTERVAL", 10)) * time.Second,
		HealthFailureWindow: time.Duration(getEnvInt("HC_FAILURE_WINDOW", 30)) * time.Second,
	}
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
	if data, err := os.ReadFile(secretPath); err == nil {
		return strings.TrimSpace(string(data))
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

// Export for bash scripts
func exportLogLevelForBash(level string) {
	switch strings.ToLower(level) {
	case "debug", "2":
		os.Setenv("_LOG_LEVEL", "2")
	case "error", "0":
		os.Setenv("_LOG_LEVEL", "0")
	default:
		os.Setenv("_LOG_LEVEL", "1")
	}
}

func setupAutoEnable() {
	if os.Getenv("PS_CLIENT") != "" || os.Getenv("PS_CMD") != "" {
		os.Setenv("PS_ENABLED", "true")
		os.Setenv("PF_ENABLED", "true")
	}
}

func (a *App) logConfig() {
	a.log.Debug("Environment configuration:")
	a.log.Debug("  PIA_LOCATION=%s", a.cfg.PIALocation)
	a.log.Debug("  LOG_LEVEL=%s", a.cfg.LogLevel)
	a.log.Debug("  IPV6_ENABLED=%v", a.cfg.IPv6Enabled)
	a.log.Debug("  LOCAL_NETWORKS=%s", a.cfg.LocalNetworks)
	a.log.Debug("  DNS=%s", a.cfg.DNS)
	a.log.Debug("  MTU=%d", a.cfg.MTU)
	a.log.Debug("  WG_BACKEND=%s", a.cfg.WGBackend)
	a.log.Debug("  IPT_BACKEND=%s", a.cfg.IPTBackend)
	a.log.Debug("  PF_ENABLED=%v", a.cfg.PFEnabled)
	a.log.Debug("  PORT_FILE=%s", a.cfg.PortFile)
	a.log.Debug("  PS_CLIENT=%s", a.cfg.PSClient)
	a.log.Debug("  PS_URL=%s", a.cfg.PSURL)
	a.log.Debug("  PS_CMD=%s", a.cfg.PSCmd)
	a.log.Debug("  PROXY_ENABLED=%v", a.cfg.ProxyEnabled)
	a.log.Debug("  SOCKS5_PORT=%d", a.cfg.Socks5Port)
	a.log.Debug("  HTTP_PROXY_PORT=%v", a.cfg.HTTPProxyPort)
	a.log.Debug("  METRICS_ENABLED=%v", a.cfg.MetricsEnabled)
	a.log.Debug("  METRICS_PORT=%d", a.cfg.MetricsPort)
	a.log.Debug("  INSTANCE_NAME=%s", a.cfg.InstanceName)
	a.log.Debug("  HC_INTERVAL=%s", a.cfg.HealthCheckInterval)
	a.log.Debug("  HC_FAILURE_WINDOW=%s", a.cfg.HealthFailureWindow)
}
