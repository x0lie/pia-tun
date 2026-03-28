package wan

import (
	"context"
	"net"
	"time"

	"github.com/x0lie/pia-tun/internal/firewall"
	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/metrics"
)

const timeout = 5 * time.Second

func Check(ctx context.Context) bool {
	logger := log.New("wan")
	logger.Trace("Checking WAN connectivity (bypass routes, parallel)")

	type result struct {
		target  string
		success bool
	}

	results := make(chan result, len(firewall.WANCheckIPs))

	for _, ip := range firewall.WANCheckIPs {
		go func(t string) {
			addr := net.JoinHostPort(t, "13")
			dialer := &net.Dialer{Timeout: timeout}
			conn, err := dialer.DialContext(ctx, "tcp", addr)
			if err == nil {
				conn.Close()
				results <- result{t, true}
			} else {
				results <- result{t, false}
			}
		}(ip)
	}

	for i := 0; i < len(firewall.WANCheckIPs); i++ {
		res := <-results
		if res.success {
			logger.Trace("WAN check successful (%s)", res.target)
			return true
		}
	}

	logger.Trace("All WAN checks failed")
	return false

}

func WaitForUp(ctx context.Context, metrics *metrics.Metrics) error {
	log.Step("Testing WAN...")

	downSince := time.Now()

	if Check(ctx) {
		log.Success("Internet up")
		metrics.UpdateWANStatus(true)
		return nil
	}

	log.Error("Internet down, waiting...")
	metrics.UpdateWANStatus(false)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
			if Check(ctx) {
				log.Success("Internet restored (down for %s)",
					log.FormatDuration(time.Since(downSince)))
				metrics.UpdateWANStatus(true)
				return nil
			}
		}
	}
}
