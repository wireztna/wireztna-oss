package version

// Set via ldflags at build time
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)
