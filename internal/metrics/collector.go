package metrics

import (
	"context"
	"time"

	"github.com/x0lie/pia-tun/internal/firewall"
	"github.com/x0lie/pia-tun/internal/wg"
)

func (m *Metrics) RunCollector(ctx context.Context, fw *firewall.Firewall) {
	m.log.Debug("Metrics collection loop starting...")
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	m.collect(fw)

	for {
		select {
		case <-ctx.Done():
			m.log.Debug("Metrics collector received shutdown signal")
			return

		case <-ticker.C:
			m.collect(fw)
		}
	}
}

func (m *Metrics) collect(fw *firewall.Firewall) {
	m.UpdateLastHandshake(wg.GetLastHandshake())

	rx, tx, _ := wg.GetTransferBytes()
	m.UpdateTransferBytes(rx, tx)

	pIn, bIn, pOut, bOut := fw.GetDropStats()
	m.UpdateKillswitchDrops(pIn, bIn, pOut, bOut)

	m.UpdateKillswitchStatus(fw.IsActive())
}
