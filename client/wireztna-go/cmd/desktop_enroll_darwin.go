//go:build darwin

package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"
	platformdarwin "github.com/wireztna/client/desktop/platform/darwin"
	"github.com/wireztna/client/internal/config"
)

const maxDesktopEnrollmentRequestBytes = 4 * 1024

type desktopEnrollmentRequest struct {
	Action string `json:"action"`
	URL    string `json:"url,omitempty"`
}

type desktopEnrollmentResponse struct {
	Success  bool   `json:"success"`
	Enrolled bool   `json:"enrolled,omitempty"`
	Email    string `json:"email,omitempty"`
	Error    string `json:"error,omitempty"`
}

func init() {
	rootCmd.AddCommand(newDesktopEnrollmentCommand())
}

func newDesktopEnrollmentCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "desktop-enroll",
		Hidden: true,
		Args:   cobra.NoArgs,
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			if runtime.GOARCH != "arm64" {
				return errors.New("desktop enrollment supports macOS arm64 only")
			}
			if os.Geteuid() == 0 {
				return errors.New("desktop enrollment must run as the signed-in desktop user")
			}
			return nil
		},
		RunE: func(_ *cobra.Command, _ []string) error {
			return runDesktopEnrollment(os.Stdin, os.Stdout)
		},
	}
}

func runDesktopEnrollment(input io.Reader, output io.Writer) error {
	request, err := decodeDesktopEnrollmentRequest(input)
	if err != nil {
		return encodeDesktopEnrollmentResponse(output, desktopEnrollmentResponse{Success: false, Error: "ENROLLMENT_INVALID_REQUEST"})
	}

	switch request.Action {
	case "context":
		dir, err := config.UserConfigDir()
		if err != nil {
			return encodeDesktopEnrollmentResponse(output, desktopEnrollmentResponse{Success: false, Error: "ENROLLMENT_CONFIG_UNAVAILABLE"})
		}
		configPath := filepath.Join(dir, "config.yaml")
		if _, err := os.Lstat(configPath); os.IsNotExist(err) {
			return encodeDesktopEnrollmentResponse(output, desktopEnrollmentResponse{Success: true, Enrolled: false})
		} else if err != nil {
			return encodeDesktopEnrollmentResponse(output, desktopEnrollmentResponse{Success: false, Error: "ENROLLMENT_CONFIG_UNAVAILABLE"})
		}
		if err := config.ReadConfigFile(configPath); err != nil {
			return encodeDesktopEnrollmentResponse(output, desktopEnrollmentResponse{Success: false, Error: "ENROLLMENT_CONFIG_UNAVAILABLE"})
		}
		loaded := config.Load()
		if err := platformdarwin.ValidateClientConfig(loaded); err != nil {
			return encodeDesktopEnrollmentResponse(output, desktopEnrollmentResponse{Success: false, Error: "ENROLLMENT_CONFIG_UNAVAILABLE"})
		}
		return encodeDesktopEnrollmentResponse(output, desktopEnrollmentResponse{
			Success:  true,
			Enrolled: true,
			Email:    loaded.LoginIdentifier(),
		})
	case "enroll":
		apiURL, token, err := parseEnrollmentURL(request.URL, true)
		if err != nil {
			return encodeDesktopEnrollmentResponse(output, desktopEnrollmentResponse{Success: false, Error: "ENROLLMENT_INVALID_URL"})
		}
		if err := config.ReadResolvedConfig(); err != nil {
			return encodeDesktopEnrollmentResponse(output, desktopEnrollmentResponse{Success: false, Error: "ENROLLMENT_CONFIG_UNAVAILABLE"})
		}
		result, err := enrollDevice(apiURL, token)
		if err != nil {
			var failure *enrollmentFailure
			if errors.As(err, &failure) {
				return encodeDesktopEnrollmentResponse(output, desktopEnrollmentResponse{Success: false, Error: failure.code})
			}
			return encodeDesktopEnrollmentResponse(output, desktopEnrollmentResponse{Success: false, Error: "ENROLLMENT_REQUEST_FAILED"})
		}
		return encodeDesktopEnrollmentResponse(output, desktopEnrollmentResponse{
			Success:  true,
			Enrolled: true,
			Email:    result.Email,
		})
	default:
		return encodeDesktopEnrollmentResponse(output, desktopEnrollmentResponse{Success: false, Error: "ENROLLMENT_INVALID_REQUEST"})
	}
}

func decodeDesktopEnrollmentRequest(input io.Reader) (desktopEnrollmentRequest, error) {
	payload, err := io.ReadAll(io.LimitReader(input, maxDesktopEnrollmentRequestBytes+1))
	if err != nil || len(payload) > maxDesktopEnrollmentRequestBytes {
		return desktopEnrollmentRequest{}, errors.New("desktop enrollment request exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var request desktopEnrollmentRequest
	if err := decoder.Decode(&request); err != nil {
		return desktopEnrollmentRequest{}, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return desktopEnrollmentRequest{}, errors.New("desktop enrollment request must contain exactly one JSON value")
	}
	return request, nil
}

func encodeDesktopEnrollmentResponse(output io.Writer, response desktopEnrollmentResponse) error {
	return json.NewEncoder(output).Encode(response)
}
