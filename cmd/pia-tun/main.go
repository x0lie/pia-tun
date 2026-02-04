package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/x0lie/pia-tun/internal/cacher"
	"github.com/x0lie/pia-tun/internal/monitor"
	"github.com/x0lie/pia-tun/internal/portforward"
	"github.com/x0lie/pia-tun/internal/proxy"
)

func main() {
	// Detect command from argv[0] (symlink name) or first argument
	cmd := filepath.Base(os.Args[0])

	switch cmd {
	case "monitor", "cacher", "portforward", "proxy":
		// Invoked via symlink (busybox-style)
	default:
		// Invoked as pia-tun <subcommand>
		if len(os.Args) < 2 {
			fmt.Fprintf(os.Stderr, "Usage: %s <monitor|cacher|portforward|proxy>\n", os.Args[0])
			os.Exit(1)
		}
		cmd = os.Args[1]
	}

	// Create signal-aware context
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)
	defer cancel()

	var err error
	switch cmd {
	case "monitor":
		err = monitor.Run(ctx)
	case "cacher":
		err = cacher.Run(ctx)
	case "portforward":
		err = portforward.Run(ctx)
	case "proxy":
		err = proxy.Run(ctx)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		fmt.Fprintf(os.Stderr, "Usage: %s <monitor|cacher|portforward|proxy>\n", os.Args[0])
		os.Exit(1)
	}

	if err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
