package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata"

	"github.com/x0lie/pia-tun/internal/app"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)
	defer cancel()

	err := app.Run(ctx)

	if err != nil && !errors.Is(err, context.Canceled) {
		os.Exit(1)
	}
}
