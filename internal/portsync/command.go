package portsync

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/x0lie/pia-tun/internal/log"
)

func executeCommand(ctx context.Context, cmd string, port int, logger *log.Logger) error {
	expanded := strings.ReplaceAll(cmd, "{PORT}", strconv.Itoa(port))

	cmdCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	logger.Debug("Executing port sync command: %s", expanded)

	out, err := exec.CommandContext(cmdCtx, "sh", "-c", expanded).CombinedOutput()
	if len(out) > 0 {
		logger.Debug("Command output: %s", strings.TrimSpace(string(out)))
	}
	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("command timed out after 10s")
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}

	return nil
}
