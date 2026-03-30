//go:build integration

package integration

import (
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/x0lie/pia-tun/internal/app"
	"github.com/x0lie/pia-tun/internal/firewall"
	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/monitor"
	"github.com/x0lie/pia-tun/internal/wan"
	"github.com/x0lie/pia-tun/internal/wg"
)

const (
	// hardcoded environment configurations
	metricsEnabled  = true
	logLevel        = "trace"
	hcInterval      = 4 * time.Second
	hcFailureWindow = 4 * time.Second

	// imported values
	ifaceName      = wg.IfaceName
	wanTimeout     = wan.Timeout
	monitorTimeout = monitor.Timeout
	chainIn        = firewall.ChainIn
	chainOut       = firewall.ChainOut
	bypassComment  = firewall.BypassComment

	// test configurations
	imageName     = "pia-tun:test"
	containerName = "pia-tun-test"
)

type container struct {
	id         string
	cfg        *app.Config
	metricsURL string
	hostIP     string
	metrics    *metricsSnapshot
	json       *jsonSnapshot
}

// projectRoot returns the absolute path to the pia-tun repo root,
// navigating up from this file's location at compile time.
func projectRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// buildImage builds the pia-tun:test Docker image from the project root.
func buildImage() error {
	cmd := exec.Command("docker", "build", "-t", imageName, "-q", ".")
	cmd.Dir = projectRoot()
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Error("docker build output:\n%s\n", out)
	}
	return err
}

// startContainer starts the pia-tun container with the given config and registers cleanup.
// Calls t.Fatal if the container fails to start.
func startContainer(t *testing.T, cfg app.Config) *container {
	t.Helper()

	exec.Command("docker", "rm", "-f", containerName).Run()

	cfg.Monitor.Interval = hcInterval
	cfg.Monitor.FailureWindow = hcFailureWindow
	cfg.LogLevel = logLevel
	cfg.Metrics.Enabled = metricsEnabled

	dns := cfg.DNSMode
	switch dns {
	case "do53", "dot":
		dns = strings.Join(cfg.DNS, ",")
	case "pia":
		dns = ""
	default:
		dns = "none"
	}

	if cfg.FW.LANs == "none" {
		cfg.FW.LANs = ""
		log.Warning("LOCAL_NETWORKS=none is not supported, using empty string")
	}

	metricsPort := strconv.Itoa(cfg.Metrics.Port)
	httpProxyPort := strconv.Itoa(cfg.Proxy.HTTPPort)
	socks5Port := strconv.Itoa(cfg.Proxy.Socks5Port)

	args := []string{
		"run", "-d",
		"--name", containerName,
		"--cap-add", "NET_ADMIN",
		"--cap-drop", "ALL",
		"--device", "/dev/net/tun:/dev/net/tun",
		"-p", metricsPort + ":" + metricsPort,
		"-e", "PIA_USER=" + cfg.PIA.User,
		"-e", "PIA_PASS=" + cfg.PIA.Pass,
		"-e", "PIA_LOCATIONS=" + cfg.PIA.Location,
		"-e", "LOG_LEVEL=" + cfg.LogLevel,
		"-e", "DNS=" + dns,
		"-e", "WG_BACKEND=" + cfg.VPN.Backend,
		"-e", "MTU=" + strconv.Itoa(cfg.VPN.MTU),
		"-e", "IPT_BACKEND=" + cfg.FW.Backend,
		"-e", "PF_ENABLED=" + strconv.FormatBool(cfg.PF.Enabled),
		"-e", "PORT_FILE=" + cfg.PF.PortFile,
		"-e", "PF_SIGNATURE_SAFETY_HOURS=" + strconv.Itoa(cfg.PF.SignatureSafetyHours),
		"-e", "LOCAL_NETWORKS=" + cfg.FW.LANs,
		"-e", "PROXY_ENABLED=" + strconv.FormatBool(cfg.Proxy.Enabled),
		"-e", "SOCKS5_PORT=" + socks5Port,
		"-e", "HTTP_PROXY_PORT=" + httpProxyPort,
		"-e", "PROXY_USER=" + cfg.Proxy.User,
		"-e", "PROXY_PASS=" + cfg.Proxy.Pass,
		"-e", "METRICS_ENABLED=" + strconv.FormatBool(cfg.Metrics.Enabled),
		"-e", "METRICS_PORT=" + metricsPort,
		"-e", "HC_INTERVAL=" + strconv.Itoa(int(hcInterval.Seconds())),
		"-e", "HC_FAILURE_WINDOW=" + strconv.Itoa(int(hcFailureWindow.Seconds())),
		"-e", "INSTANCE_NAME=" + cfg.Metrics.Name,
	}

	if cfg.Proxy.Enabled {
		args = append(args,
			"-p", httpProxyPort+":"+httpProxyPort,
			"-p", socks5Port+":"+socks5Port,
		)
	}

	if cfg.FW.Backend == "legacy" {
		args = append(args, "--cap-add", "NET_RAW")
	}

	args = append(args, imageName)

	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		fatal(t, "failed to start container: %v\n%s", err, out)
	}

	ctr := &container{
		id:         strings.TrimSpace(string(out)),
		cfg:        &cfg,
		metricsURL: "http://localhost:" + metricsPort,
	}
	return ctr
}

// stop removes the container.
func (c *container) stop(t *testing.T) {
	t.Helper()
	if t.Failed() {
		out, _ := exec.Command("docker", "logs", c.id).CombinedOutput()
		log.Step("Container logs:\n%s", out)
	}
	exec.Command("docker", "rm", "-f", c.id).Run()
}

// tryExec runs a command inside the container without fataling on failure.
// Returns output and whether the command succeeded.
func (c *container) tryExec(args ...string) (string, bool) {
	cmd := append([]string{"exec", c.id}, args...)
	out, err := exec.Command("docker", cmd...).CombinedOutput()
	return strings.TrimSpace(string(out)), err == nil
}

// exec runs a command inside the container and returns combined output.
func (c *container) exec(t *testing.T, args ...string) string {
	t.Helper()
	cmd := append([]string{"exec", c.id}, args...)
	out, err := exec.Command("docker", cmd...).CombinedOutput()
	if err != nil {
		fatal(t, "exec %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (c *container) deleteInterface(t *testing.T) {
	t.Helper()
	c.exec(t, "ip", "link", "delete", ifaceName)
	log.Success("%s deleted", ifaceName)
}

// emulateWANDown simulates ISP outage by removing iptables rules by comment bypassRoutes
func (c *container) emulateWANDown(t *testing.T) {
	t.Helper()

	c.exec(t, "sh", "-c", fmt.Sprintf(
		`while true; do
			num=$(iptables -L %s --line-numbers -n | grep -m 1 "%s" | awk '{print $1}')
			[ -z "$num" ] && break
			iptables -D %s "$num" 2>/dev/null || break
		done`,
		chainOut, bypassComment, chainOut,
	))
	log.Success("bypass_routes removed from %s", chainOut)
}

var ipServices = []string{
	"https://api.ipify.org",
	"https://icanhazip.com",
	"https://ifconfig.me",
}

func (c *container) getHostIP(t *testing.T) {
	t.Helper()
	for _, url := range ipServices {
		resp := c.httpGet(t, url)
		if ip := strings.TrimSpace(string(resp)); ip != "" {
			c.hostIP = ip
			return
		}
	}
	fatal(t, "Could not get host IP from any service")
}

func (c *container) getVPNIP(t *testing.T) {
	t.Helper()
	for _, url := range ipServices {
		ip, pass := c.tryExec("curl", "-s", url)
		if !pass || net.ParseIP(ip) == nil {
			continue
		}
		if ip == c.hostIP {
			fatal(t, "VPN IP == host IP")
		}
		log.Success("VPN IP: %s", ip)
		return
	}
	fatal(t, "Could not get VPN IP from any service")
}

func (c *container) verifyPort(t *testing.T) {
	portFromFile := c.exec(t, "cat", c.cfg.PF.PortFile)
	if portFromFile == "" {
		fatal(t, "could not read portfile: %s", c.cfg.PF.PortFile)
	}

	portFromMetrics := c.getMetric(t, "pia_tun_port_forwarding_port")

	if portFromFile != portFromMetrics {
		fatal(t, "portFromFile (%s) != portFromMetrics (%s)", portFromFile, portFromMetrics)
	}
	log.Success("metrics port matches port file")

	rules := c.exec(t, "iptables", "-L", chainIn, "-n")
	for _, comment := range firewall.PortForwardComments {
		if !strings.Contains(rules, comment) || !strings.Contains(rules, portFromFile) {
			fatal(t, "%s missing %s rule for port %s", chainIn, comment, portFromFile)
		}
	}
	log.Success("%s has tcp+udp ACCEPT rules for port %s", chainIn, portFromFile)
}

func (c *container) testProxy(t *testing.T) {
	t.Helper()

	socksPort := strconv.Itoa(c.cfg.Proxy.Socks5Port)
	httpPort := strconv.Itoa(c.cfg.Proxy.HTTPPort)

	socksTarget := "localhost:" + socksPort
	httpTarget := "http://localhost:" + httpPort

	authEnabled := c.cfg.Proxy.User != "" && c.cfg.Proxy.Pass != ""
	if authEnabled {
		userPass := c.cfg.Proxy.User + ":" + c.cfg.Proxy.Pass
		socksTarget = userPass + "@localhost:" + socksPort
		httpTarget = "http://" + userPass + "@localhost:" + httpPort
	}

	// SOCKS5
	if _, ok := c.tryExec("nc", "-z", "localhost", socksPort); !ok {
		fatal(t, "SOCKS5 not listening on :%s", socksPort)
	}
	socksIP, ok := c.tryExec("curl", "-sf", "--max-time", "10", "--socks5", socksTarget, "ifconfig.me")
	if !ok {
		fatal(t, "failed to curl through SOCKS5 proxy")
	}
	if socksIP == c.hostIP {
		fatal(t, "SOCKS5 IP same as host IP")
	} else {
		log.Success("SOCKS5 routes through VPN (%s)", socksIP)
	}
	if authEnabled {
		if _, ok := c.tryExec("curl", "-sf", "--max-time", "5", "--socks5", "localhost:"+socksPort, "ifconfig.me"); ok {
			fatal(t, "SOCKS5 accepted unauthenticated connection")
		} else {
			log.Success("SOCKS5 auth enforced")
		}
	}

	// HTTP proxy
	if _, ok := c.tryExec("nc", "-z", "localhost", httpPort); !ok {
		fatal(t, "HTTP proxy not listening on :%s", httpPort)
	}
	httpIP, ok := c.tryExec("curl", "-sf", "--max-time", "10", "--proxy", httpTarget, "http://ifconfig.me")
	if !ok {
		fatal(t, "failed to curl through HTTP proxy")
	}
	if httpIP == c.hostIP {
		fatal(t, "HTTP proxy IP same as hostIP")
	} else {
		log.Success("HTTP proxy routes through VPN (%s)", httpIP)
	}
	if authEnabled {
		if _, ok := c.tryExec("curl", "-sf", "--max-time", "5", "--proxy", "http://localhost:"+httpPort, "http://ifconfig.me"); ok {
			fatal(t, "HTTP proxy accepted unauthenticated connection")
		} else {
			log.Success("HTTP proxy auth enforced")
		}
	}
}

func (c *container) testForLeaks(t *testing.T) {
	t.Helper()

	var check sync.WaitGroup
	check.Add(1)
	go func() {
		defer check.Done()
		if _, ok := c.tryExec("ping", "-q", "-c", "1", "-W", "3", "1.0.0.1"); ok {
			fail(t, "ICMP to 1.0.0.1 succeeded — traffic leaking")
		} else {
			log.Success("ICMP blocked")
		}
	}()

	check.Add(1)
	go func() {
		defer check.Done()
		if _, ok := c.tryExec("curl", "-sf", "--max-time", "3", "ifconfig.me"); ok {
			fail(t, "curl to ifconfig.me succeeded — traffic leaking")
		} else {
			log.Success("TCP blocked")
		}
	}()

	if c.cfg.DNSMode == "pia" {
		check.Add(1)
		go func() {
			defer check.Done()
			if _, ok := c.tryExec("nslookup", "-timeout=3", "example.com"); ok {
				fail(t, "DNS resolves with VPN down — possible leak")
			} else {
				log.Success("DNS blocked")
			}
		}()
	}
	check.Wait()
}

func indentLines(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return "      " + strings.Join(lines, "\n      ")
}

func (c *container) logFirewallState(t *testing.T) {
	args := []string{"iptables", "--list-rules"}
	log.Info("  iptables --list-rules\n%s\n", indentLines(c.exec(t, args...)))

	args = []string{"ip6tables", "--list-rules"}
	log.Info("  ip6tables --list-rules\n%s\n", indentLines(c.exec(t, args...)))

	args = []string{"ip", "-4", "rule", "list"}
	log.Info("  ip -4 rule list\n%s\n", indentLines(c.exec(t, args...)))

	args = []string{"ip", "-6", "rule", "list"}
	log.Info("  ip -6 rule list\n%s\n", indentLines(c.exec(t, args...)))
}
