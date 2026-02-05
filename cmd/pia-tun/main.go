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
	"github.com/x0lie/pia-tun/internal/portforward"
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
		err = monitor.Run(ctx, nil)
	case "cacher":
		err = cacher.Run(ctx)
	case "portforward":
		err = portforward.Run(ctx, nil)
	case "proxy":
		err = proxy.Run(ctx)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		fmt.Fprintf(os.Stderr, "Usage: %s [monitor|cacher|portforward|proxy]\n", os.Args[0])
		os.Exit(1)
	}

	if err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
