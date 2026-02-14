package portsync

import (
	"context"
	"fmt"
	"time"

	"github.com/x0lie/pia-tun/internal/log"
)

type Client interface {
	SyncPort(ctx context.Context, port int) error
	Name() string
}

type Config struct {
	Client string
	URL    string
	User   string
	Pass   string
	Cmd    string
}

type Syncer struct {
	client Client
	cmd    string
	log    *log.Logger
	portCh chan int
}

func New(cfg Config, logger *log.Logger) *Syncer {
	s := &Syncer{
		cmd:    cfg.Cmd,
		log:    logger,
		portCh: make(chan int, 1),
	}

	// Resolve default URL from client type
	if cfg.URL == "" {
		cfg.URL = defaultURL(cfg.Client)
	}

	// Create the appropriate client implementation
	switch normalizeClient(cfg.Client) {
	case "qbittorrent":
		s.client = newQBittorrent(cfg.URL, cfg.User, cfg.Pass, logger)
	case "transmission":
		s.client = newTransmission(cfg.URL, cfg.User, cfg.Pass, logger)
	case "deluge":
		s.client = newDeluge(cfg.URL, cfg.Pass, logger)
	}

	return s
}

// NotifyPort sends a new port to the syncer. Non-blocking.
// Called by the port forwarding keepalive manager when port is obtained/changed.
func (s *Syncer) NotifyPort(port int) {
	// Drain any pending port to avoid blocking
	select {
	case <-s.portCh:
	default:
	}
	s.portCh <- port
}

// Run is the main loop. Blocks until context is cancelled.
func (s *Syncer) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case port := <-s.portCh:
			s.handleNewPort(ctx, port)
		}
	}
}

func (s *Syncer) handleNewPort(ctx context.Context, port int) {
	clientOK := s.client == nil // true if no client configured (nothing to do)
	cmdOK := s.cmd == ""        // true if no cmd configured

	// Initial attempt
	clientOK, cmdOK = s.trySync(ctx, port, clientOK, cmdOK)
	if clientOK && cmdOK {
		return
	}

	// Log first failure
	if s.client != nil && !clientOK {
		log.Warning(fmt.Sprintf("%s not reachable, will retry", s.client.Name()))
	}
	if s.cmd != "" && !cmdOK {
		log.Warning("port sync command failed, will retry")
	}

	// Retry loop — exits on success, new port, or context cancellation
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case newPort := <-s.portCh:
			// New port arrived — abandon this one, handle the new one
			s.handleNewPort(ctx, newPort)
			return
		case <-timer.C:
			clientOK, cmdOK = s.trySync(ctx, port, clientOK, cmdOK)
			if clientOK && cmdOK {
				return
			}
			timer.Reset(30 * time.Second)
		}
	}
}

// trySync attempts sync for only the methods that haven't succeeded yet.
func (s *Syncer) trySync(ctx context.Context, port int, clientOK, cmdOK bool) (bool, bool) {
	if s.client != nil && !clientOK {
		if err := s.client.SyncPort(ctx, port); err != nil {
			s.log.Debug("%s sync failed: %v", s.client.Name(), err)
		} else {
			log.Success(fmt.Sprintf("[%s] %s port updated",
				time.Now().Format("2006-01-02 15:04:05"), s.client.Name()))
			clientOK = true
		}
	}

	if s.cmd != "" && !cmdOK {
		if err := executeCommand(ctx, s.cmd, port, s.log); err != nil {
			s.log.Debug("command failed: %v", err)
		} else {
			log.Success(fmt.Sprintf("[%s] port sync command completed",
				time.Now().Format("2006-01-02 15:04:05")))
			cmdOK = true
		}
	}

	return clientOK, cmdOK
}

func normalizeClient(ct string) string {
	switch ct {
	case "qbittorrent", "qbit", "qb":
		return "qbittorrent"
	case "transmission", "trans":
		return "transmission"
	case "deluge":
		return "deluge"
	default:
		return ct
	}
}

func defaultURL(Client string) string {
	switch normalizeClient(Client) {
	case "qbittorrent":
		return "http://localhost:8080"
	case "transmission":
		return "http://localhost:9091"
	case "deluge":
		return "http://localhost:8112"
	default:
		return ""
	}
}
