//go:build !windows

package main

// openWizard is a no-op on non-Windows platforms.
// macOS/Linux users use the CLI: wireztna enroll / wireztna login / wireztna connect
func openWizard(startStep string, onDone func()) {
	// No-op: GUI wizard is Windows-only
	_ = startStep
	_ = onDone
}
