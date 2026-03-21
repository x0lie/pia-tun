package log

import (
	"fmt"
	"strings"
	"time"
)

const (
	ColorReset  = "\033[0m"
	ColorBold   = "\033[1m"
	ColorRed    = "\033[0;31m"
	ColorGreen  = "\033[0;32m"
	ColorBlue   = "\033[0;34m"
	ColorYellow = "\033[0;33m"
	ColorCyan   = "\033[0;36m"
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
	timestamp := time.Now().Format("15:04:05.000")
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("    %s[DEBUG]%s %s - %-12s %s\n", ColorBlue, ColorReset, timestamp, l.Prefix+":", msg)
}

func (l *Logger) Trace(format string, args ...any) {
	if Level < 3 {
		return
	}
	timestamp := time.Now().Format("15:04:05.000")
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("    %s[TRACE]%s %s - %-12s %s\n", ColorYellow, ColorReset, timestamp, l.Prefix+":", msg)
}

// UI output — shown at info level (1) and above
func Success(format string, args ...any) {
	if Level < 1 {
		return
	}
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("  %s\u2713%s %s\n", ColorGreen, ColorReset, msg)
}

func Step(format string, args ...any) {
	if Level < 1 {
		return
	}
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("\n%s\u25b6%s %s\n", ColorBlue, ColorReset, msg)
}

func Info(format string, args ...any) {
	if Level < 1 {
		return
	}
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("  %s\n", msg)
}

// Always shown (level 0+)
func Error(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("  %s\u2717%s %s\n", ColorRed, ColorReset, msg)
}

func Warning(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
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

func StartupBanner(version, sha string) {
	if Level < 1 {
		return
	}

	versionText := "pia-tun " + version
	if len(version) > 0 && version[0] >= '0' && version[0] <= '9' {
		versionText = "pia-tun v" + version
	}

	c, r, g, b := ColorCyan, ColorReset, ColorGreen, ColorBold

	out := fmt.Sprintf("\n%s\n%s\n%s\n%s\n%s\n%s\n",
		boxTop(c),
		boxEmpty(c),
		boxCenter(c, b+versionText+r, len(versionText)),
		boxCenter(c, g+"x0lie"+r, 5),
		boxEmpty(c),
		boxBottom(c),
	)

	if version == "develop" && sha != "" {
		out += fmt.Sprintf(" commit: %s\n", sha)
	}

	fmt.Print(out)
}

func ConnectedBanner() {
	if Level < 1 {
		return
	}
	c, r, b := ColorGreen, ColorReset, ColorBold
	fmt.Printf("\n%s\n%s\n%s\n",
		boxTop(c),
		boxCenter(c, c+"✓"+r+" "+b+"VPN Connected"+r, 15),
		boxBottom(c),
	)
}

func ReconnectingBanner() {
	if Level < 1 {
		return
	}
	c, r, b := ColorYellow, ColorReset, ColorBold
	fmt.Printf("\n%s\n%s\n%s\n",
		boxTop(c),
		boxCenter(c, c+"↻"+r+" "+b+"Reconnecting VPN"+r, 18),
		boxBottom(c),
	)
}

const boxBorder = "════════════════════════════════════════════════"

func boxTop(color string) string {
	return color + "╔" + boxBorder + "╗" + ColorReset
}

func boxBottom(color string) string {
	return color + "╚" + boxBorder + "╝" + ColorReset
}

func boxEmpty(color string) string {
	return color + "║" + ColorReset + strings.Repeat(" ", 48) + color + "║" + ColorReset
}

func boxCenter(color, content string, visibleLen int) string {
	const inner = 48
	left := (inner - visibleLen) / 2
	right := inner - visibleLen - left
	return color + "║" + ColorReset +
		strings.Repeat(" ", left) + content + strings.Repeat(" ", right) +
		color + "║" + ColorReset
}
