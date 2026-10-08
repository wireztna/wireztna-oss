//go:build windows

package config

// realUserHome on Windows is a no-op.
// Windows doesn't have the sudo HOME-reset problem — UAC elevation
// preserves USERPROFILE, and the service uses ProgramData (shared dir).
func realUserHome() string {
	return ""
}
