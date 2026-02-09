package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/x0lie/pia-tun/internal/app"
	"github.com/x0lie/pia-tun/internal/cacher"
	"github.com/x0lie/pia-tun/internal/monitor"
	"github.com/x0lie/pia-tun/internal/proxy"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)
	defer cancel()

	// Detect command from argv[0] (symlink name) or first argument.
	// When invoked as "pia-tun" with no arguments, run the full orchestrator.
	cmd := filepath.Base(os.Args[0])

	switch cmd {
	case "monitor", "cacher", "portforward", "proxy":
		// Invoked via symlink (busybox-style) - run individual component
	default:
		if len(os.Args) >= 2 {
			cmd = os.Args[1]
		} else {
			cmd = "" // no subcommand = run full orchestrator
		}
	}

	var err error
	switch cmd {
	case "":
		err = app.Run(ctx)
	case "monitor":
		err = monitor.Run(ctx, nil, nil)
	case "cacher":
		err = cacher.Run(ctx, nil)
	case "portforward":
		fmt.Fprintln(os.Stderr, "Standalone portforward mode is no longer supported.")
		fmt.Fprintln(os.Stderr, "Port forwarding runs automatically when PF_ENABLED=true in the orchestrator.")
		os.Exit(1)
	case "proxy":
		err = proxy.Run(ctx)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		fmt.Fprintf(os.Stderr, "Usage: %s [monitor|cacher|portforward|proxy]\n", os.Args[0])
		os.Exit(1)
	}

	if err != nil && err != context.Canceled {
		os.Exit(1)
	}
}
