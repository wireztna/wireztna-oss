// Package debug provides client-side diagnostic information for the TUI.
package debug

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Info holds all debug diagnostic data from the client side.
type Info struct {
	WireGuard string // wg show output
	Routes    string // Routes through the WG interface
	DNS       string // Split DNS configuration
	Timestamp time.Time
}

// LogEntry represents a connection event.
type LogEntry struct {
	Time    time.Time
	Level   string // "info", "error", "warn", "debug"
	Message string
}

// Logger keeps an in-memory ring buffer of connection events.
type Logger struct {
	mu      sync.Mutex
	entries []LogEntry
	maxSize int
}

// NewLogger creates a logger with the given max capacity.
func NewLogger(maxSize int) *Logger {
	return &Logger{maxSize: maxSize, entries: make([]LogEntry, 0, maxSize)}
}

// Log adds an entry.
func (l *Logger) Log(level, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.entries) >= l.maxSize {
		l.entries = l.entries[1:]
	}
	l.entries = append(l.entries, LogEntry{Time: time.Now(), Level: level, Message: msg})
}

// Info logs an info message.
func (l *Logger) Info(msg string) { l.Log("info", msg) }

// Error logs an error message.
func (l *Logger) Error(msg string) { l.Log("error", msg) }

// Warn logs a warning message.
func (l *Logger) Warn(msg string) { l.Log("warn", msg) }

// Debug logs a verbose debug message.
func (l *Logger) Debug(msg string) { l.Log("debug", msg) }

// Entries returns a copy of all log entries.
func (l *Logger) Entries() []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	cp := make([]LogEntry, len(l.entries))
	copy(cp, l.entries)
	return cp
}

// EntriesFiltered returns entries matching the given minimum level.
// Levels: debug < info < warn < error
func (l *Logger) EntriesFiltered(minLevel string) []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	order := map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}
	min := order[minLevel]
	var filtered []LogEntry
	for _, e := range l.entries {
		if order[e.Level] >= min {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// Count returns total log entries.
func (l *Logger) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// Gather collects all debug info from the local system.
func Gather(ifaceName string) Info {
	return Info{
		WireGuard: getWGStatus(ifaceName),
		Routes:    getRoutes(ifaceName),
		DNS:       getDNSConfig(),
		Timestamp: time.Now(),
	}
}

func run(cmd string, args ...string) string {
	out, err := exec.Command(cmd, args...).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("(error: %v)", err)
	}
	return strings.TrimSpace(string(out))
}

func getWGStatus(ifaceName string) string {
	switch runtime.GOOS {
	case "darwin":
		// Try wg show first, fallback to listing devices
		out := run("wg", "show", ifaceName)
		if strings.Contains(out, "error") || out == "" {
			out = run("wg", "show")
		}
		return out
	case "linux":
		return run("wg", "show", ifaceName)
	case "windows":
		return run("wg", "show", ifaceName)
	default:
		return "(unsupported OS)"
	}
}

func getRoutes(ifaceName string) string {
	switch runtime.GOOS {
	case "darwin":
		// Get routes via netstat, filter for the utun interface used by WG
		out := run("netstat", "-rn")
		var lines []string
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "utun") || strings.Contains(line, ifaceName) || strings.Contains(line, "10.200") || strings.Contains(line, "10.50") || strings.Contains(line, "10.0") {
				lines = append(lines, line)
			}
		}
		if len(lines) == 0 {
			return "(no WireGuard routes found)"
		}
		return strings.Join(lines, "\n")
	case "linux":
		out := run("ip", "route", "show", "dev", ifaceName)
		if out == "" {
			return "(no routes on " + ifaceName + ")"
		}
		return out
	case "windows":
		out := run("route", "print")
		var lines []string
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "10.200") || strings.Contains(line, "10.50") || strings.Contains(line, "10.0") || strings.Contains(line, "WireGuard") {
				lines = append(lines, strings.TrimSpace(line))
			}
		}
		if len(lines) == 0 {
			return "(no WireGuard routes found)"
		}
		return strings.Join(lines, "\n")
	default:
		return "(unsupported OS)"
	}
}

func getDNSConfig() string {
	switch runtime.GOOS {
	case "darwin":
		// Check /etc/resolver/ files
		out := run("ls", "/etc/resolver/")
		if strings.Contains(out, "error") || out == "" {
			return "(no split DNS configured)"
		}
		var result strings.Builder
		result.WriteString("Resolver files:\n")
		for _, file := range strings.Split(out, "\n") {
			file = strings.TrimSpace(file)
			if file == "" {
				continue
			}
			content := run("cat", "/etc/resolver/"+file)
			result.WriteString(fmt.Sprintf("  %s → %s\n", file, extractNameserver(content)))
		}
		return result.String()
	case "linux":
		out := run("resolvectl", "status", "wg-wireztna")
		if strings.Contains(out, "error") || out == "" {
			return "(no split DNS configured — resolvectl not available or interface not found)"
		}
		return out
	case "windows":
		out := run("powershell", "-NoProfile", "-Command", "Get-DnsClientNrptRule | Where-Object { $_.DisplayName -like 'WireZTNA:*' } | Format-Table -AutoSize")
		if strings.Contains(out, "error") || out == "" {
			return "(no NRPT rules configured)"
		}
		return out
	default:
		return "(unsupported OS)"
	}
}

func extractNameserver(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "nameserver") {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "nameserver"))
		}
	}
	return "?"
}
