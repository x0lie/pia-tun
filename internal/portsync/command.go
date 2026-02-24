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

func executeScript(ctx context.Context, script string, port int, logger *log.Logger) error {
	expanded := strings.ReplaceAll(script, "{PORT}", strconv.Itoa(port))

	scriptCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	logger.Debug("Executing port sync script: %s", expanded)

	out, err := exec.CommandContext(scriptCtx, "sh", "-c", expanded).CombinedOutput()
	if len(out) > 0 {
		logger.Debug("Script output: %s", strings.TrimSpace(string(out)))
	}
	if err != nil {
		if scriptCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("script timed out after 10s")
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}

	return nil
}
