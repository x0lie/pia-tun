package log

import (
	"fmt"
	"os"
	"time"
)

const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[0;31m"
	ColorGreen  = "\033[0;32m"
	ColorBlue   = "\033[0;34m"
	ColorYellow = "\033[0;33m"
	ColorBold   = "\033[1m"
)

// Logger provides debug logging with a component prefix.
type Logger struct {
	Enabled bool
	Prefix  string // e.g. "cacher", "monitor"
}

// Debug logs a debug message to stderr with timestamp and optional prefix.
func (l *Logger) Debug(format string, args ...interface{}) {
	if !l.Enabled {
		return
	}
	timestamp := time.Now().Format("15:04:05")
	msg := fmt.Sprintf(format, args...)
	if l.Prefix != "" {
		fmt.Fprintf(os.Stderr, "    %s[DEBUG]%s %s - %s: %s\n", ColorBlue, ColorReset, timestamp, l.Prefix, msg)
	} else {
		fmt.Fprintf(os.Stderr, "    %s[DEBUG]%s %s - %s\n", ColorBlue, ColorReset, timestamp, msg)
	}
}

func Success(msg string) {
	fmt.Printf("  %s\u2713%s %s\n", ColorGreen, ColorReset, msg)
}

func Error(msg string) {
	fmt.Printf("  %s\u2717%s %s\n", ColorRed, ColorReset, msg)
}

func Warning(msg string) {
	fmt.Printf("  %s\u26a0%s %s\n", ColorYellow, ColorReset, msg)
}

func Step(msg string) {
	fmt.Printf("\n%s\u25b6%s %s\n", ColorBlue, ColorReset, msg)
}

func Info(msg string) {
	fmt.Printf("  %s\n", msg)
}

func Blank() {
	fmt.Println()
}

// FormatDuration formats a duration into a human-readable string.
func FormatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	} else if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}
