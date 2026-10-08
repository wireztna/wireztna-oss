package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/wireztna/client/internal/api"
	"github.com/wireztna/client/internal/config"
	"golang.org/x/term"
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with the control plane",
	Long: `Authenticates with the WireZTNA control plane.

Authentication priority:
1. SSO/OIDC (if enabled on server) — opens browser for Microsoft sign-in
2. OTP (default) — sends a one-time code to your registered email
3. Password (--password flag) — traditional username/password

The token is stored at ~/.wireztna/token with 0600 permissions.`,
	RunE: runLogin,
}

func init() {
	rootCmd.AddCommand(loginCmd)
	loginCmd.Flags().StringP("email", "e", "", "email address for login")
	loginCmd.Flags().StringP("username", "u", "", "username (backward compat, prefer --email)")
	loginCmd.Flags().Bool("local", false, "force local login (skip SSO check)")
	loginCmd.Flags().Bool("password", false, "use password authentication instead of OTP")
}

func runLogin(cmd *cobra.Command, args []string) error {
	apiURL := viper.GetString("api_url")
	if apiURL == "" {
		return fmt.Errorf("API URL required: set --api-url flag, WIREZTNA_API_URL env, or api_url in config")
	}

	forceLocal, _ := cmd.Flags().GetBool("local")
	usePassword, _ := cmd.Flags().GetBool("password")
	client := api.NewClient(apiURL)

	// Check if server supports OIDC (unless user forces local login)
	if !forceLocal && !usePassword {
		oidcConfig, err := client.GetOIDCConfig()
		if err == nil && oidcConfig.Enabled {
			return runOIDCLogin(client)
		}
		// If OIDC check fails or is disabled, fall through to OTP
	}

	// Password mode (legacy, explicit flag required)
	if usePassword {
		return runPasswordLogin(cmd, client)
	}

	// Default: OTP flow
	return runOTPLogin(cmd, client)
}

// getLoginIdentifier resolves the email/username for login.
// Priority: --email flag > --username flag > config email > config username > interactive prompt.
func getLoginIdentifier(cmd *cobra.Command) string {
	email, _ := cmd.Flags().GetString("email")
	if email != "" {
		return email
	}
	username, _ := cmd.Flags().GetString("username")
	if username != "" {
		return username
	}
	// Try config (email first, then username for old configs)
	if e := viper.GetString("email"); e != "" {
		return e
	}
	if u := viper.GetString("username"); u != "" {
		return u
	}
	// Interactive prompt
	fmt.Print("Email: ")
	var input string
	fmt.Scanln(&input)
	return input
}

// runOTPLogin performs OTP-based authentication (code sent to email).
func runOTPLogin(cmd *cobra.Command, client *api.Client) error {
	identifier := getLoginIdentifier(cmd)

	// Step 1: Request OTP code
	fmt.Printf("[*] Requesting access code for %s...\n", identifier)
	otpResp, err := client.OTPRequest(identifier)
	if err != nil {
		return fmt.Errorf("failed to request code: %w", err)
	}

	if otpResp.EmailHint != "" {
		fmt.Printf("[*] Code sent to %s (valid %d min)\n", otpResp.EmailHint, otpResp.ExpiresIn/60)
	} else {
		fmt.Printf("[*] If the account exists, a code has been sent (valid %d min)\n", otpResp.ExpiresIn/60)
	}

	// Step 2: Prompt for code
	fmt.Print("\nEnter code: ")
	var code string
	fmt.Scanln(&code)

	if code == "" {
		return fmt.Errorf("no code entered")
	}

	// Step 3: Verify code
	token, err := client.OTPVerify(identifier, code)
	if err != nil {
		return fmt.Errorf("verification failed: %w", err)
	}

	if err := config.SaveToken(token); err != nil {
		return fmt.Errorf("failed to cache token: %w", err)
	}

	fmt.Println("[+] Authenticated successfully — token cached")

	// On Windows, try to launch the tray app if it's installed but not running
	launchTrayIfInstalled()

	return nil
}

// runPasswordLogin performs email/password authentication (legacy, use --password flag).
func runPasswordLogin(cmd *cobra.Command, client *api.Client) error {
	identifier := getLoginIdentifier(cmd)

	password := viper.GetString("password")
	if password == "" {
		fmt.Print("Password: ")
		pwBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
		if err != nil {
			return fmt.Errorf("failed to read password: %w", err)
		}
		fmt.Println()
		password = string(pwBytes)
	}

	token, err := client.Login(identifier, password)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	if err := config.SaveToken(token); err != nil {
		return fmt.Errorf("failed to cache token: %w", err)
	}

	fmt.Println("[+] Authenticated successfully — token cached")

	// On Windows, try to launch the tray app if it's installed but not running
	launchTrayIfInstalled()

	return nil
}

// runOIDCLogin performs device authorization flow (SSO via Microsoft Entra ID).
func runOIDCLogin(client *api.Client) error {
	fmt.Println("[*] SSO is enabled — starting device authorization...")

	// Step 1: Start device flow
	deviceResp, err := client.OIDCDeviceStart()
	if err != nil {
		return fmt.Errorf("failed to start SSO flow: %w", err)
	}

	// Step 2: Show instructions to user
	fmt.Println()
	fmt.Printf("  Open this URL in your browser:\n")
	fmt.Printf("  \033[1m%s\033[0m\n", deviceResp.VerificationURI)
	fmt.Println()
	fmt.Printf("  Enter code: \033[1;36m%s\033[0m\n", deviceResp.UserCode)
	fmt.Println()

	// Try to open browser automatically
	if url := deviceResp.VerificationURIComplete; url != "" {
		openBrowser(url)
	} else {
		openBrowser(deviceResp.VerificationURI)
	}

	// Step 3: Poll until user completes auth or timeout
	interval := time.Duration(deviceResp.Interval) * time.Second
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(deviceResp.ExpiresIn) * time.Second)

	fmt.Print("  Waiting for authorization")
	for time.Now().Before(deadline) {
		time.Sleep(interval)
		fmt.Print(".")

		pollResp, err := client.OIDCDevicePoll(deviceResp.DeviceCode)
		if err != nil {
			fmt.Println()
			return fmt.Errorf("SSO poll error: %w", err)
		}

		switch pollResp.Status {
		case "completed":
			fmt.Println()
			// Save the token
			if err := config.SaveToken(pollResp.AccessToken); err != nil {
				return fmt.Errorf("failed to cache token: %w", err)
			}
			fmt.Printf("[+] Authenticated as %s — token cached\n", pollResp.Username)

			// On Windows, try to launch the tray app
			launchTrayIfInstalled()
			return nil

		case "expired":
			fmt.Println()
			return fmt.Errorf("device code expired — please try again")

		case "denied":
			fmt.Println()
			return fmt.Errorf("authorization was denied")

		case "pending":
			// Continue polling
			continue

		default:
			fmt.Println()
			return fmt.Errorf("unexpected status: %s", pollResp.Status)
		}
	}

	fmt.Println()
	return fmt.Errorf("timed out waiting for authorization")
}

// openBrowser attempts to open a URL in the user's default browser.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return
	}
	// Fire and forget — don't block if browser fails to open
	_ = cmd.Start()
}
