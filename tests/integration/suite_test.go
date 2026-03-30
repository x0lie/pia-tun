//go:build integration

package integration

import (
	"os"
	"testing"
	"time"

	"github.com/x0lie/pia-tun/internal/app"
	"github.com/x0lie/pia-tun/internal/log"
)

// Note: tests assume iptables-nft compatibility;
// iptables-legacy will give a false positive result

func TestMain(m *testing.M) {
	log.StartupBanner("test-suite", "")
	log.Step("Building image...")
	if err := buildImage(); err != nil {
		log.Error("failed to build image: %v\n", err)
		os.Exit(1)
	}
	log.Success("Image built")
	log.Info("")
	os.Exit(m.Run())
}

func TestSuite(t *testing.T) {
	cfg := app.LoadConfig()

	log.Step("Starting container...")
	ctr := startContainer(t, cfg)
	log.Success("Container up")
	t.Cleanup(func() { ctr.stop(t) })

	// Initial Run
	log.Step("Waiting for endpoints...")
	startTime := time.Now()
	ctr.waitForReady(t, startTime)
	ctr.waitForHealthy(t, startTime)
	if cfg.PF.Enabled {
		ctr.waitForPortForward(t, startTime)
		ctr.verifyPort(t)
	}
	log.Step("Verifying initial metrics...")
	ctr.testInitialUpMetrics(t)
	log.Step("Verifying IPs...")
	ctr.getHostIP(t)
	log.Success("host IP: %s", ctr.hostIP)
	ctr.getVPNIP(t)
	log.Step("Testing proxy...")
	if cfg.Proxy.Enabled {
		ctr.testProxy(t)
	}
	log.Step("Runtime firewall stats:")
	ctr.logFirewallState(t)

	// Reconnect
	log.Step("Testing reconnect...")
	ctr.deleteInterface(t)
	startTime = time.Now()
	ctr.testForLeaks(t)
	log.Info("  waiting for reconnect (%v)...", maxDowntime)
	ctr.waitForUnhealthy(t, startTime)
	startTime = time.Now()
	ctr.waitForHealthy(t, startTime)
	if cfg.PF.Enabled {
		ctr.waitForPortForward(t, startTime)
		ctr.verifyPort(t)
	}
	log.Step("Verifying reconnect metrics...")
	ctr.testReconnectMetrics(t)

	// Down state
	log.Step("Testing down state...")
	ctr.emulateWANDown(t)
	ctr.deleteInterface(t)
	startTime = time.Now()
	ctr.testForLeaks(t)
	log.Info("  waiting for down state (%v)...", maxTimeUntilWANDown)
	ctr.waitForWANDown(t, startTime)
	ctr.testDowntimeMetrics(t)
	log.Step("Downtime firewall stats:")
	ctr.logFirewallState(t)
}

func fail(t *testing.T, format string, args ...any) {
	t.Helper()
	log.Error(format, args...)
	t.Fail()
}

func fatal(t *testing.T, format string, args ...any) {
	t.Helper()
	log.Error(format, args...)
	t.FailNow()
}
