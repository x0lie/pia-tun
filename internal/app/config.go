package app

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	LogLevel string

	IPv6Enabled    bool
	PFEnabled      bool
	ProxyEnabled   bool
	MetricsEnabled bool

	LocalNetworks string
	LocalPorts    string
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
	return Config{
		LogLevel: getEnv("LOG_LEVEL", "info"),

		IPv6Enabled:    getEnvBool("IPV6_ENABLED", false),
		PFEnabled:      getEnvBool("PF_ENABLED", false),
		ProxyEnabled:   getEnvBool("PROXY_ENABLED", false),
		MetricsEnabled: getEnvBool("METRICS", true),

		LocalNetworks: getEnv("LOCAL_NETWORKS", ""),
		LocalPorts:    getEnv("LOCAL_PORTS", ""),
		DNS:           getEnv("DNS", "pia"),

		MTU: getEnvInt("MTU", 1420),

		WGBackend:  getEnv("WG_BACKEND", ""),
		IPTBackend: getEnv("IPT_BACKEND", ""),

		PortFile: getEnv("PORT_FILE", "/run/pia-tun/port"),

		PSClient: getEnv("PS_CLIENT", ""),
		PSURL:    getEnv("PS_URL", ""),
		PSUser:   getEnv("PS_USER", ""),
		PSPass:   getEnv("PS_PASS", ""),
		PSCmd:    getEnv("PS_CMD", ""),

		ProxyUser:     getEnv("PROXY_USER", ""),
		ProxyPass:     getEnv("PROXY_PASS", ""),
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
