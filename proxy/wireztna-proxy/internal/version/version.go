// Package version holds build-time version information injected via ldflags.
package version

// Set at build time via -ldflags.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)
