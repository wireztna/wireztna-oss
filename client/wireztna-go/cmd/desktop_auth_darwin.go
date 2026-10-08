//go:build darwin

package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/mail"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wireztna/client/internal/api"
	"github.com/wireztna/client/internal/config"
)

const maxDesktopAuthRequestBytes = 4 * 1024

type desktopAuthRequest struct {
	Action string `json:"action"`
	Email  string `json:"email,omitempty"`
	Code   string `json:"code,omitempty"`
}

type desktopAuthResponse struct {
	Success bool   `json:"success"`
	Email   string `json:"email,omitempty"`
	Error   string `json:"error,omitempty"`
}

type desktopAuthAPI interface {
	OTPRequest(string) (*api.OTPRequestResponse, error)
	OTPVerify(string, string) (string, error)
}

var newDesktopAuthAPI = func(baseURL string) desktopAuthAPI {
	return api.NewClient(baseURL)
}

func init() {
	rootCmd.AddCommand(newDesktopAuthCommand())
}

func newDesktopAuthCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "desktop-auth",
		Hidden: true,
		Args:   cobra.NoArgs,
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			if runtime.GOARCH != "arm64" {
				return errors.New("desktop authentication supports macOS arm64 only")
			}
			if os.Geteuid() == 0 {
				return errors.New("desktop authentication must run as the signed-in desktop user")
			}
			return initConfig()
		},
		RunE: func(_ *cobra.Command, _ []string) error {
			return runDesktopAuth(os.Stdin, os.Stdout)
		},
	}
}

func runDesktopAuth(input io.Reader, output io.Writer) error {
	request, err := decodeDesktopAuthRequest(input)
	if err != nil {
		return encodeDesktopAuthResponse(output, desktopAuthResponse{Success: false, Error: "AUTH_INVALID_REQUEST"})
	}

	cfg := config.Load()
	email := strings.TrimSpace(request.Email)
	if email == "" {
		email = strings.TrimSpace(cfg.LoginIdentifier())
	}
	apiURL, ok := canonicalDesktopAuthAPIURL(cfg.APIURL)
	if !ok {
		return encodeDesktopAuthResponse(output, desktopAuthResponse{Success: false, Error: "AUTH_CONFIG_UNAVAILABLE"})
	}

	switch request.Action {
	case "context":
		return encodeDesktopAuthResponse(output, desktopAuthResponse{Success: true, Email: email})
	case "request_otp":
		if !validDesktopAuthEmail(email) {
			return encodeDesktopAuthResponse(output, desktopAuthResponse{Success: false, Error: "AUTH_INVALID_EMAIL"})
		}
		if _, err := newDesktopAuthAPI(apiURL).OTPRequest(email); err != nil {
			return encodeDesktopAuthResponse(output, desktopAuthResponse{Success: false, Error: "AUTH_REQUEST_FAILED"})
		}
		return encodeDesktopAuthResponse(output, desktopAuthResponse{Success: true, Email: email})
	case "verify_otp":
		if !validDesktopAuthEmail(email) {
			return encodeDesktopAuthResponse(output, desktopAuthResponse{Success: false, Error: "AUTH_INVALID_EMAIL"})
		}
		if !validDesktopAuthCode(request.Code) {
			return encodeDesktopAuthResponse(output, desktopAuthResponse{Success: false, Error: "AUTH_INVALID_OTP"})
		}
		token, err := newDesktopAuthAPI(apiURL).OTPVerify(email, request.Code)
		if errors.Is(err, api.ErrMFARequired) {
			return encodeDesktopAuthResponse(output, desktopAuthResponse{Success: false, Error: "AUTH_MFA_REQUIRED"})
		}
		if err != nil {
			return encodeDesktopAuthResponse(output, desktopAuthResponse{Success: false, Error: "AUTH_VERIFY_FAILED"})
		}
		if token == "" || config.IsTokenExpired(token) {
			return encodeDesktopAuthResponse(output, desktopAuthResponse{Success: false, Error: "AUTH_TOKEN_INVALID"})
		}
		if err := config.SaveToken(token); err != nil {
			return encodeDesktopAuthResponse(output, desktopAuthResponse{Success: false, Error: "AUTH_TOKEN_SAVE_FAILED"})
		}
		return encodeDesktopAuthResponse(output, desktopAuthResponse{Success: true, Email: email})
	default:
		return encodeDesktopAuthResponse(output, desktopAuthResponse{Success: false, Error: "AUTH_INVALID_REQUEST"})
	}
}

func decodeDesktopAuthRequest(input io.Reader) (desktopAuthRequest, error) {
	payload, err := io.ReadAll(io.LimitReader(input, maxDesktopAuthRequestBytes+1))
	if err != nil {
		return desktopAuthRequest{}, err
	}
	if len(payload) > maxDesktopAuthRequestBytes {
		return desktopAuthRequest{}, errors.New("desktop auth request exceeds size limit")
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var request desktopAuthRequest
	if err := decoder.Decode(&request); err != nil {
		return desktopAuthRequest{}, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return desktopAuthRequest{}, errors.New("desktop auth request must contain exactly one JSON value")
	}
	return request, nil
}

func encodeDesktopAuthResponse(output io.Writer, response desktopAuthResponse) error {
	return json.NewEncoder(output).Encode(response)
}

func canonicalDesktopAuthAPIURL(value string) (string, bool) {
	if value == "" || strings.TrimSpace(value) != value {
		return "", false
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") ||
		parsed.Port() == "8443" {
		return "", false
	}
	portText := parsed.Port()
	if portText != "" {
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != portText || port == 8443 {
			return "", false
		}
	}
	return "https://" + parsed.Host, true
}

func validDesktopAuthEmail(value string) bool {
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address == value
}

func validDesktopAuthCode(value string) bool {
	if len(value) != 6 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
