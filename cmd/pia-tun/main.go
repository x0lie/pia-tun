package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/x0lie/pia-tun/internal/cacher"
	"github.com/x0lie/pia-tun/internal/monitor"
	"github.com/x0lie/pia-tun/internal/portforward"
	"github.com/x0lie/pia-tun/internal/proxy"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)
	defer cancel()

	if os.Getenv("PIA_TUN_NEW_ENTRYPOINT") != "1" {
		cmd := exec.Command("/app/run.sh")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin

		cmd.Env = append(os.Environ(), "PIA_TUN_NEW_ENTRYPOINT=1")

		// Put child in its own process group
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Setpgid: true,
		}

		if err := cmd.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		// Forward signals
		go func() {
			<-ctx.Done()
			// Send SIGTERM to the entire process group
			pgid, _ := syscall.Getpgid(cmd.Process.Pid)
			syscall.Kill(-pgid, syscall.SIGTERM)
		}()

		err := cmd.Wait()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				os.Exit(exitErr.ExitCode())
			}
			os.Exit(1)
		}

		return
	}

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
