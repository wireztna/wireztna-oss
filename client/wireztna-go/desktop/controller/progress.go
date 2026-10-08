package controller

import "fmt"

// ProgressStage is a stable, ordered controller operation stage.
type ProgressStage string

const (
	ProgressStageAuthenticating       ProgressStage = "authenticating"
	ProgressStageRequestingAccess     ProgressStage = "requesting_access"
	ProgressStageConfiguringWireGuard ProgressStage = "configuring_wireguard"
	ProgressStageConfiguringRoutes    ProgressStage = "configuring_routes"
	ProgressStageConfiguringDNS       ProgressStage = "configuring_dns"
	ProgressStageVerifyingConnection  ProgressStage = "verifying_connection"
	ProgressStageConnected            ProgressStage = "connected"
)

// Progress is the structured payload of an operation_progress event.
type Progress struct {
	Stage ProgressStage
}

// Validate rejects missing or unknown progress stages.
func (p Progress) Validate() error {
	if !p.Stage.valid() {
		return fmt.Errorf("unknown progress stage %q", p.Stage)
	}
	return nil
}

func (s ProgressStage) valid() bool {
	switch s {
	case ProgressStageAuthenticating,
		ProgressStageRequestingAccess,
		ProgressStageConfiguringWireGuard,
		ProgressStageConfiguringRoutes,
		ProgressStageConfiguringDNS,
		ProgressStageVerifyingConnection,
		ProgressStageConnected:
		return true
	default:
		return false
	}
}
