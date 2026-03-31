//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/x0lie/pia-tun/internal/log"
)

const (
	machineBuffer       = 5 * time.Second
	maxDowntime         = hcInterval + hcFailureWindow + monitorTimeout + machineBuffer
	maxTimeUntilWANDown = maxDowntime + wanTimeout
	readyTimeout        = 5 * time.Second
	healthyTimeout      = 10 * time.Second
	pfTimeout           = 15 * time.Second
)

type metricsSnapshot struct {
	connectionUp           bool
	wanUp                  bool
	killswitchActive       bool
	latencyCount           float64
	healthChecksTotal      int64
	healthChecksSuccessful int64
	pfActive               bool
	pfPort                 int
	reconnectsTotal        int
	bytesReceived          float64
	bytesTransmitted       float64
	lastHandshakeTimestamp int64
}

type jsonSnapshot struct {
	currentServer string
	currentIP     string
}

func parseMetrics(body, name string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, name+" ") && !strings.HasPrefix(line, "#") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				return fields[1]
			}
		}
	}
	return ""
}

// pollUntil polls cond every 100ms until it returns true or the timeout expires.
func pollUntil(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	timer := time.NewTimer(timeout)
	defer ticker.Stop()
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			fatal(t, "timed out waiting for %s", desc)
		case <-ticker.C:
			if cond() {
				return
			}
		}
	}
}

// httpGet retries http-dependents like fetchMetrics and getJSON to avoid transient failures
func (c *container) httpGet(t *testing.T, url string) []byte {
	t.Helper()
	var body []byte
	pollUntil(t, 500*time.Millisecond, url, func() bool {
		resp, err := http.Get(url)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		body, _ = io.ReadAll(resp.Body)
		return true
	})
	return body
}

// fetchMetrics returns the raw Prometheus metrics text.
func (c *container) fetchMetrics(t *testing.T) string {
	t.Helper()
	body := c.httpGet(t, c.metricsURL+"/metrics")
	return string(body)
}

// getJSON fetches the JSON metrics endpoint and stores the result in c.json.
func (c *container) getJSON(t *testing.T) {
	t.Helper()
	body := c.httpGet(t, c.metricsURL+"/metrics?format=json")

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		fatal(t, "getJSON: invalid JSON: %v", err)
	}

	get := func(field string) string {
		v, ok := raw[field]
		if !ok {
			return ""
		}
		return strings.Trim(string(v), `"`)
	}

	c.json = &jsonSnapshot{
		currentServer: get("current_server"),
		currentIP:     get("current_ip"),
	}
}

// getMetric fetches a single Prometheus metric value by name prefix.
func (c *container) getMetric(t *testing.T, name string) string {
	t.Helper()
	return parseMetrics(c.fetchMetrics(t), name)
}

// getMetrics fetches all metrics and returns metrics struct
func (c *container) getMetrics(t *testing.T) {
	t.Helper()
	body := c.fetchMetrics(t)
	p := func(name string) string { return parseMetrics(body, name) }

	latency, _ := strconv.ParseFloat(p("pia_tun_server_latency_seconds_count"), 64)
	pfPort, _ := strconv.Atoi(p("pia_tun_port_forwarding_port"))
	hcTotal, _ := strconv.ParseInt(p("pia_tun_health_checks_total"), 10, 64)
	hcSuccessful, _ := strconv.ParseInt(p("pia_tun_health_checks_successful_total"), 10, 64)
	reconnTotal, _ := strconv.Atoi(p("pia_tun_reconnects_total"))
	bytesReceived, _ := strconv.ParseFloat(p("pia_tun_bytes_received_total"), 64)
	bytesTransmitted, _ := strconv.ParseFloat(p("pia_tun_bytes_transmitted_total"), 64)
	lastHandshakeTimestamp, _ := strconv.ParseInt(p("pia_tun_last_handshake_timestamp_seconds"), 10, 64)

	c.metrics = &metricsSnapshot{
		connectionUp:           p("pia_tun_connection_up") == "1",
		wanUp:                  p("pia_tun_wan_up") == "1",
		killswitchActive:       p("pia_tun_killswitch_active") == "1",
		latencyCount:           latency,
		pfActive:               p("pia_tun_port_forwarding_active") == "1",
		pfPort:                 pfPort,
		healthChecksTotal:      hcTotal,
		healthChecksSuccessful: hcSuccessful,
		reconnectsTotal:        reconnTotal,
		bytesReceived:          bytesReceived,
		bytesTransmitted:       bytesTransmitted,
		lastHandshakeTimestamp: lastHandshakeTimestamp,
	}
}

func (c *container) waitForEndpoint(t *testing.T, path string, ok bool, timeout time.Duration) {
	t.Helper()
	desiredCode := http.StatusOK
	if !ok {
		desiredCode = http.StatusServiceUnavailable
	}
	pollUntil(t, timeout, path, func() bool {
		resp, _ := (&http.Client{Timeout: 2 * time.Second}).Get(c.metricsURL + path)
		if resp == nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode == desiredCode {
			return true
		}
		out, _ := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", c.id).Output()
		if strings.TrimSpace(string(out)) != "true" {
			fatal(t, "container exited while waiting for %s", path)
		}
		return false
	})
}

// waitForReady polls /ready until the metrics server responds.
func (c *container) waitForReady(t *testing.T, startTime time.Time) {
	t.Helper()
	ready := true
	c.waitForEndpoint(t, "/ready", ready, readyTimeout-time.Since(startTime))
	log.Success("/ready responding ok (%.1fs)", time.Since(startTime).Seconds())
}

// waitForHealthy polls /health until the VPN is connected.
func (c *container) waitForHealthy(t *testing.T, startTime time.Time) {
	t.Helper()
	healthy := true
	c.waitForEndpoint(t, "/health", healthy, healthyTimeout-time.Since(startTime))
	log.Success("/health responding ok (%.1fs)", time.Since(startTime).Seconds())
}

func (c *container) waitForUnhealthy(t *testing.T, startTime time.Time) {
	t.Helper()
	healthy := false
	c.waitForEndpoint(t, "/health", healthy, maxDowntime-time.Since(startTime))
	log.Success("/health responding !ok (%.1fs)", time.Since(startTime).Seconds())
}

// waitForWANDown polls metrics until _wan_up == 0.
func (c *container) waitForWANDown(t *testing.T, startTime time.Time) {
	t.Helper()
	pollUntil(t, maxTimeUntilWANDown-time.Since(startTime), "wan down", func() bool {
		return c.getMetric(t, "pia_tun_wan_up") == "0"
	})
	log.Success("pia_tun_wan_up == 0 (%.1fs)", time.Since(startTime).Seconds())
}

// waitForPortForward polls until port forwarding is active.
func (c *container) waitForPortForward(t *testing.T, startTime time.Time) {
	t.Helper()
	pollUntil(t, pfTimeout-time.Since(startTime), "port forwarding", func() bool {
		return c.getMetric(t, "pia_tun_port_forwarding_active") == "1"
	})
	log.Success("port forwarding active (%.1fs)", time.Since(startTime).Seconds())
}

func (c *container) testInitialUpMetrics(t *testing.T) {
	t.Helper()
	c.getMetrics(t)
	c.getJSON(t)

	if c.json.currentServer == "" || c.json.currentIP == "" {
		fatal(t, "missing server (%q) or ip (%q) in metrics", c.json.currentServer, c.json.currentIP)
	}
	log.Success("connected to: %s (%s)", c.json.currentServer, c.json.currentIP)

	if !c.metrics.connectionUp {
		fatal(t, "pia_tun_connection_up != 1")
	}
	log.Success("pia_tun_connection_up == 1")

	if !c.metrics.wanUp {
		fatal(t, "pia_tun_wan_up != 1")
	}
	log.Success("pia_tun_wan_up == 1")

	if !c.metrics.killswitchActive {
		fatal(t, "pia_tun_killswitch_active != 1")
	}
	log.Success("pia_tun_killswitch_active == 1")

	if c.metrics.healthChecksTotal < 1 {
		fatal(t, "pia_tun_health_checks_total < 1")
	}
	log.Success("pia_tun_health_checks_total == %d", c.metrics.healthChecksTotal)

	if c.metrics.latencyCount == 0 {
		fatal(t, "pia_tun_server_latency_seconds_count == 0")
	}
	log.Success("pia_tun_server_latency_seconds_count == %v", c.metrics.latencyCount)

	if !c.cfg.PF.Enabled {
		return
	}

	if !c.metrics.pfActive {
		fatal(t, "pia_tun_port_forwarding_active == 0")
	}
	log.Success("pia_tun_port_forwarding_active == 1")

	if c.metrics.pfPort == 0 {
		fatal(t, "pia_tun_port_forwarding_port == 0")
	}
	log.Success("pia_tun_port_forwarding_port == %d", c.metrics.pfPort)
}

func (c *container) testReconnectMetrics(t *testing.T) {
	t.Helper()
	reconnectsTotal := c.getMetric(t, "pia_tun_reconnects_total")

	if reconnectsTotal != "1" {
		fatal(t, "pia_tun_reconnects_total != 1")
	}
	log.Success("pia_tun_reconnects_total == 1")

	c.testInitialUpMetrics(t)
}

func (c *container) testDowntimeMetrics(t *testing.T) {
	t.Helper()
	c.getMetrics(t)

	if c.metrics.connectionUp {
		fatal(t, "pia_tun_connection_up != 0")
	}
	log.Success("pia_tun_connection_up == 0")

	if c.metrics.wanUp {
		fatal(t, "pia_tun_wan_up != 0")
	}
	log.Success("pia_tun_wan_up == 0")

	if !c.metrics.killswitchActive {
		fatal(t, "pia_tun_killswitch_active != 1")
	}
	log.Success("pia_tun_killswitch_active == 1")

	if !c.cfg.PF.Enabled {
		return
	}

	if c.metrics.pfActive {
		fatal(t, "pia_tun_port_forwarding_active != 0")
	}
	log.Success("pia_tun_port_forwarding_active == 0")

	if c.metrics.pfPort != 0 {
		fatal(t, "pia_tun_port_forwarding_port != 0")
	}
	log.Success("pia_tun_port_forwarding_port == %d", c.metrics.pfPort)
}
