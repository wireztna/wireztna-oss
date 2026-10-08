// Package version holds build-time version info injected via ldflags.
package version

import "fmt"

// Set via -ldflags at build time.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

func Full() string {
	return fmt.Sprintf("wireztna %s (commit: %s, built: %s)", Version, Commit, BuildDate)
}
