package log

import (
	"fmt"
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

// Level controls global log verbosity.
// Set once at startup, read by all logging functions.
var Level int = 1

// Logger provides prefixed diagnostic logging for a component.
type Logger struct {
	Prefix string
}

func New(prefix string) *Logger {
	return &Logger{Prefix: prefix}
}

func (l *Logger) Debug(format string, args ...any) {
	if Level < 2 {
		return
	}
	timestamp := time.Now().Format("15:04:05")
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("    %s[DEBUG]%s %s - %-12s %s\n", ColorBlue, ColorReset, timestamp, l.Prefix+":", msg)
}

func (l *Logger) Trace(format string, args ...any) {
	if Level < 3 {
		return
	}
	timestamp := time.Now().Format("15:04:05")
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("    %s[TRACE]%s %s - %-12s %s\n", ColorYellow, ColorReset, timestamp, l.Prefix+":", msg)
}

// UI output — shown at info level (1) and above
func Success(msg string) {
	if Level < 1 {
		return
	}
	fmt.Printf("  %s\u2713%s %s\n", ColorGreen, ColorReset, msg)
}

func Step(msg string) {
	if Level < 1 {
		return
	}
	fmt.Printf("\n%s\u25b6%s %s\n", ColorBlue, ColorReset, msg)
}

func Info(msg string) {
	if Level < 1 {
		return
	}
	fmt.Printf("  %s\n", msg)
}

// Always shown (level 0+)
func Error(msg string) {
	fmt.Printf("  %s\u2717%s %s\n", ColorRed, ColorReset, msg)
}

func Warning(msg string) {
	fmt.Printf("  %s\u26a0%s %s\n", ColorYellow, ColorReset, msg)
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
