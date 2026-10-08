package cmd

import (
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/wireztna/client/internal/api"
	"github.com/wireztna/client/internal/config"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

var enrollCmd = &cobra.Command{
	Use:   "enroll <url_or_token>",
	Short: "Enroll this device using a one-time token",
	Long: `Enrolls this device with the WireZTNA control plane using a one-time
enrollment token provided by an administrator.

The private WireGuard key is generated and persisted locally before the request.
If local persistence fails after the server accepts the token, rerunning the same
command safely reuses that key and the server returns the same enrollment result.`,
	Args: cobra.ExactArgs(1),
	RunE: runEnroll,
}

type enrollmentAPI interface {
	Enroll(token, publicKey string) (*api.EnrollResponse, error)
}

var newEnrollmentAPI = func(baseURL string) enrollmentAPI {
	return api.NewClient(baseURL)
}

type enrollmentFailure struct {
	code string
	err  error
}

func (failure *enrollmentFailure) Error() string { return failure.err.Error() }
func (failure *enrollmentFailure) Unwrap() error { return failure.err }

func init() {
	rootCmd.AddCommand(enrollCmd)
}

func runEnroll(cmd *cobra.Command, args []string) error {
	apiURL, token, err := parseEnrollmentInput(args[0], func() string {
		value, _ := cmd.Flags().GetString("api-url")
		return value
	}(), false)
	if err != nil {
		return err
	}

	fmt.Println("[*] WireZTNA Client Enrollment")
	result, err := enrollDevice(apiURL, token)
	if err != nil {
		return err
	}
	fmt.Println("[+] Enrollment successful!")
	fmt.Printf("    Email:      %s\n", result.Email)
	fmt.Printf("    Overlay IP: %s\n", result.OverlayIP)
	fmt.Printf("    Broker:     %s\n", result.BrokerEndpoint)
	fmt.Printf("    Networks:   %s\n", strings.Join(result.AllowedIPs, ", "))
	fmt.Println("[*] Next: run `wireztna login`, then connect from the desktop app or CLI.")
	return nil
}

func parseEnrollmentInput(input, explicitAPIURL string, requireHTTPS bool) (string, string, error) {
	if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
		return parseEnrollmentURL(input, requireHTTPS)
	}
	if !validEnrollmentToken(input) {
		return "", "", errors.New("invalid enrollment token")
	}
	apiURL, ok := canonicalEnrollmentAPIURL(explicitAPIURL, requireHTTPS)
	if !ok {
		return "", "", errors.New("a canonical external API URL is required with a token-only enrollment")
	}
	return apiURL, input, nil
}

func parseEnrollmentURL(value string, requireHTTPS bool) (string, string, error) {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 2048 {
		return "", "", errors.New("invalid enrollment URL")
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.User != nil || parsed.Fragment != "" || parsed.Path != "/api/v1/clients/enroll" {
		return "", "", errors.New("invalid enrollment URL")
	}
	apiURL, ok := canonicalEnrollmentAPIURL(parsed.Scheme+"://"+parsed.Host, requireHTTPS)
	if !ok {
		return "", "", errors.New("invalid enrollment API URL")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(query) != 1 || len(query["token"]) != 1 || !validEnrollmentToken(query["token"][0]) {
		return "", "", errors.New("enrollment URL must contain exactly one valid token")
	}
	return apiURL, query["token"][0], nil
}

func canonicalEnrollmentAPIURL(value string, requireHTTPS bool) (string, bool) {
	if value == "" || strings.TrimSpace(value) != value {
		return "", false
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") || (parsed.Scheme != "https" && (requireHTTPS || parsed.Scheme != "http")) {
		return "", false
	}
	portText := parsed.Port()
	if portText != "" {
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != portText || port == 8443 {
			return "", false
		}
	}
	return parsed.Scheme + "://" + parsed.Host, true
}

func validEnrollmentToken(value string) bool {
	if len(value) < 32 || len(value) > 256 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func enrollDevice(apiURL, token string) (*api.EnrollResponse, error) {
	privateKey, err := enrollmentPrivateKey()
	if err != nil {
		return nil, &enrollmentFailure{code: "ENROLLMENT_KEY_FAILED", err: err}
	}
	response, err := newEnrollmentAPI(apiURL).Enroll(token, privateKey.PublicKey().String())
	if err != nil {
		return nil, &enrollmentFailure{code: "ENROLLMENT_REQUEST_FAILED", err: fmt.Errorf("enrollment failed: %w", err)}
	}
	if err := validateEnrollmentResponse(response); err != nil {
		return nil, &enrollmentFailure{code: "ENROLLMENT_RESPONSE_INVALID", err: err}
	}
	enrollConfig := &config.EnrollConfig{
		APIURL:          apiURL,
		Email:           response.Email,
		Username:        response.Username,
		PrivateKey:      privateKey.String(),
		OverlayIP:       response.OverlayIP,
		BrokerPublicKey: response.BrokerPublicKey,
		BrokerEndpoint:  response.BrokerEndpoint,
		BrokerOverlayIP: response.BrokerOverlayIP,
		AllowedIPs:      append([]string(nil), response.AllowedIPs...),
		DNSServer:       response.DNSServer,
	}
	if err := config.WriteEnrollConfig(enrollConfig); err != nil {
		return nil, &enrollmentFailure{code: "ENROLLMENT_SAVE_FAILED", err: fmt.Errorf("write enrollment config: %w", err)}
	}
	if err := config.ClearPendingEnrollmentPrivateKey(); err != nil {
		return nil, &enrollmentFailure{code: "ENROLLMENT_SAVE_FAILED", err: fmt.Errorf("clear pending enrollment key: %w", err)}
	}
	return response, nil
}

func enrollmentPrivateKey() (wgtypes.Key, error) {
	if loaded := config.Load(); loaded != nil && loaded.PrivateKey != "" {
		if key, err := wgtypes.ParseKey(loaded.PrivateKey); err == nil {
			return key, nil
		}
	}
	pending, err := config.LoadPendingEnrollmentPrivateKey()
	if err != nil {
		return wgtypes.Key{}, err
	}
	if pending != "" {
		return wgtypes.ParseKey(pending)
	}
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return wgtypes.Key{}, fmt.Errorf("generate WireGuard private key: %w", err)
	}
	if err := config.SavePendingEnrollmentPrivateKey(key.String()); err != nil {
		return wgtypes.Key{}, fmt.Errorf("persist pending WireGuard private key: %w", err)
	}
	return key, nil
}

func validateEnrollmentResponse(response *api.EnrollResponse) error {
	if response == nil || !validEnrollmentText(response.Username, 255) || !validEnrollmentEmail(response.Email) {
		return errors.New("enrollment response contains an invalid identity")
	}
	if _, err := wgtypes.ParseKey(response.BrokerPublicKey); err != nil {
		return errors.New("enrollment response contains an invalid broker public key")
	}
	if _, err := netip.ParseAddr(response.OverlayIP); err != nil {
		return errors.New("enrollment response contains an invalid overlay IP")
	}
	if _, err := netip.ParseAddr(response.BrokerOverlayIP); err != nil {
		return errors.New("enrollment response contains an invalid broker overlay IP")
	}
	if _, err := netip.ParseAddr(response.DNSServer); err != nil {
		return errors.New("enrollment response contains an invalid DNS server")
	}
	host, portText, err := net.SplitHostPort(response.BrokerEndpoint)
	if err != nil || host == "" {
		return errors.New("enrollment response contains an invalid broker endpoint")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != portText {
		return errors.New("enrollment response contains an invalid broker endpoint port")
	}
	if len(response.AllowedIPs) == 0 || len(response.AllowedIPs) > 4096 {
		return errors.New("enrollment response contains an invalid allowed IP list")
	}
	for _, prefix := range response.AllowedIPs {
		if _, err := netip.ParsePrefix(prefix); err != nil {
			return errors.New("enrollment response contains an invalid allowed IP prefix")
		}
	}
	return nil
}

func validEnrollmentText(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validEnrollmentEmail(value string) bool {
	if !validEnrollmentText(value, 320) {
		return false
	}
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address == value
}
