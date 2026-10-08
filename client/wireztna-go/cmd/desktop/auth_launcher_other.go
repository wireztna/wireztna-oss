//go:build !windows

package main

// Non-Windows builds retain their existing platform-specific wizard behavior.
func openAuthFlow(step string, onDone func()) {
	openWizard(step, onDone)
}
