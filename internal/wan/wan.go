package wan

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/metrics"
)

type Checker struct {
	DialTimeout  time.Duration
	PollInterval time.Duration
}

func (c *Checker) Check(ctx context.Context) bool {
	logger := &log.Logger{
		Enabled: os.Getenv("_LOG_LEVEL") == "2",
		Prefix:  "wan",
	}
	logger.Debug("Checking WAN connectivity (bypass routes, parallel)")

	targets := []string{
		"129.6.15.28:13",
		"129.6.15.29:13",
		"132.163.96.1:13",
		"132.163.97.1:13",
		"128.138.140.44:13",
	}

	type result struct {
		target  string
		success bool
	}

	results := make(chan result, len(targets))

	for _, target := range targets {
		go func(t string) {
			dialer := &net.Dialer{Timeout: 5 * time.Second}
			conn, err := dialer.Dial("tcp", t)
			if err == nil {
				conn.Close()
				results <- result{t, true}
			} else {
				results <- result{t, false}
			}
		}(target)
	}

	for i := 0; i < len(targets); i++ {
		res := <-results
		if res.success {
			logger.Debug("WAN check successful (%s)", res.target)
			return true
		}
	}

	logger.Debug("All WAN checks failed")
	return false

}

func (c *Checker) WaitForUp(ctx context.Context, metrics *metrics.Metrics) error {
	log.Step("Testing WAN...")

	downSince := time.Now()

	if c.Check(ctx) {
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
			if c.Check(ctx) {
				log.Success(fmt.Sprintf("Internet restored (down for %s)",
					log.FormatDuration(time.Since(downSince))))
				metrics.UpdateWANStatus(true)
				return nil
			}
		}
	}
}
