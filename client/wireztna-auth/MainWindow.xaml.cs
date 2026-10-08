using System.ComponentModel;
using System.Reflection;
using System.Windows;
using System.Windows.Automation.Peers;
using System.Windows.Controls;
using System.Windows.Input;
using System.Windows.Media;
using System.Windows.Threading;
using WireZTNA.Auth.Infrastructure;
using WireZTNA.Auth.Models;

namespace WireZTNA.Auth;

public partial class MainWindow : Window
{
    private static readonly Brush DefaultOtpBorderBrush = new SolidColorBrush(Color.FromRgb(42, 47, 79));
    private static readonly Brush FilledOtpBorderBrush = new SolidColorBrush(Color.FromRgb(124, 106, 255));

    private readonly WireZtnaPipeClient _pipeClient = new();
    private readonly CancellationTokenSource _lifetimeCancellation = new();
    private readonly string? _initialStep;
    private TextBox[] _otpBoxes = [];
    private CancellationTokenSource? _activeOperation;
    private Func<Task>? _retryAction;
    private string _maskedEmail = string.Empty;
    private string? _pendingActivationStep;
    private bool _hasPendingActivation;
    private bool _mutationInProgress;
    private bool _settingOtpText;

    public MainWindow(string? initialStep)
    {
        InitializeComponent();
        _initialStep = SingleInstanceCoordinator.NormalizeStep(initialStep);
        VersionText.Text = $"WireZTNA v{GetDisplayVersion()}";

        Loaded += MainWindow_Loaded;
        ConfigureOtpInputs();
    }

    private async void MainWindow_Loaded(object sender, RoutedEventArgs e)
    {
        Loaded -= MainWindow_Loaded;
        await ReconcileWithServiceAsync(_initialStep);
    }

    public async Task ActivateFromAnotherInstanceAsync(string? requestedStep)
    {
        if (WindowState == WindowState.Minimized)
        {
            WindowState = WindowState.Normal;
        }

        Show();
        Activate();
        Topmost = true;
        Topmost = false;
        Focus();

        var normalizedStep = SingleInstanceCoordinator.NormalizeStep(requestedStep);
        if (_mutationInProgress)
        {
            _pendingActivationStep = normalizedStep;
            _hasPendingActivation = true;
            return;
        }

        await ReconcileWithServiceAsync(normalizedStep);
    }

    private async Task ReconcileWithServiceAsync(string? requestedStep)
    {
        var token = BeginOperation();
        ShowConnecting("Checking your device…", "Contacting the local WireZTNA service.");

        try
        {
            var response = await _pipeClient.GetStatusAsync(token);
            var state = EnsureSuccess(response, "Unable to read WireZTNA status.");

            if (state is null)
            {
                ShowRequestedFallback(requestedStep);
                return;
            }

            if (state.NeedsEnroll)
            {
                ShowEnroll();
            }
            else if (state.NeedsLogin)
            {
                ShowLogin(state.Email);
            }
            else if (string.Equals(state.Status, "connected", StringComparison.OrdinalIgnoreCase))
            {
                ShowSuccess();
            }
            else if (IsTransitionalStatus(state.Status))
            {
                await WaitForExistingConnectionAsync(token);
            }
            else
            {
                // Authentication UI is not a tunnel-mode controller. Opening it
                // while another controller is switching modes must never turn a
                // disconnected/error snapshot into an implicit split connect.
                ShowDisconnected(state.ErrorMessage);
            }
        }
        catch (OperationCanceledException) when (token.IsCancellationRequested)
        {
            // Superseded by activation, another action, or window shutdown.
        }
        catch (Exception ex) when (IsExpectedOperationException(ex))
        {
            ShowFatalError(ex.Message, () => ReconcileWithServiceAsync(requestedStep));
        }
    }

    private void ShowRequestedFallback(string? requestedStep)
    {
        if (requestedStep == "enroll")
        {
            ShowEnroll();
        }
        else
        {
            ShowLogin(null);
        }
    }

    private async void EnrollButton_Click(object sender, RoutedEventArgs e)
    {
        var enrollmentUrl = EnrollmentUrlBox.Text.Trim();
        if (string.IsNullOrWhiteSpace(enrollmentUrl))
        {
            ShowInlineError(EnrollErrorBorder, EnrollErrorText, "Enter the enrollment URL supplied by your administrator.");
            EnrollmentUrlBox.Focus();
            return;
        }

        _mutationInProgress = true;
        var token = BeginOperation();
        SetEnrollBusy(true);
        HideInlineError(EnrollErrorBorder);
        var enrollmentCompleted = false;

        try
        {
            var enrollResponse = await _pipeClient.EnrollAsync(enrollmentUrl, token);
            var enrollState = EnsureSuccess(enrollResponse, "Enrollment failed.");
            enrollmentCompleted = true;
            EnrollmentUrlBox.Clear();
            _maskedEmail = enrollState?.Email ?? string.Empty;

            EnrollButton.Content = "Sending access code…";
            var loginResponse = await _pipeClient.LoginAsync(token);
            var loginState = EnsureSuccess(loginResponse, "The device was enrolled, but the access code could not be sent.");

            _maskedEmail = loginState?.Email ?? enrollState?.Email ?? string.Empty;
            ShowOtp(_maskedEmail);
        }
        catch (OperationCanceledException) when (token.IsCancellationRequested)
        {
            // Superseded or closing.
        }
        catch (Exception ex) when (IsExpectedOperationException(ex))
        {
            if (enrollmentCompleted)
            {
                ShowLogin(_maskedEmail);
                ShowInlineError(LoginErrorBorder, LoginErrorText, ex.Message);
            }
            else
            {
                ShowInlineError(EnrollErrorBorder, EnrollErrorText, ex.Message);
            }
        }
        finally
        {
            SetEnrollBusy(false);
            await CompleteMutationAsync();
        }
    }

    private async void LoginButton_Click(object sender, RoutedEventArgs e)
    {
        await RequestOtpAsync(showLoginErrors: true);
    }

    private async void ResendButton_Click(object sender, RoutedEventArgs e)
    {
        await RequestOtpAsync(showLoginErrors: false);
    }

    private async Task RequestOtpAsync(bool showLoginErrors)
    {
        _mutationInProgress = true;
        var token = BeginOperation();
        SetLoginBusy(true);
        SetOtpBusy(true);
        HideInlineError(LoginErrorBorder);
        HideInlineError(OtpErrorBorder);

        try
        {
            var response = await _pipeClient.LoginAsync(token);
            var state = EnsureSuccess(response, "Unable to send an access code.");
            _maskedEmail = state?.Email ?? _maskedEmail;
            ShowOtp(_maskedEmail);
        }
        catch (OperationCanceledException) when (token.IsCancellationRequested)
        {
            // Superseded or closing.
        }
        catch (Exception ex) when (IsExpectedOperationException(ex))
        {
            if (showLoginErrors)
            {
                ShowLogin(_maskedEmail);
                ShowInlineError(LoginErrorBorder, LoginErrorText, ex.Message);
            }
            else
            {
                ShowInlineError(OtpErrorBorder, OtpErrorText, ex.Message);
            }
        }
        finally
        {
            SetLoginBusy(false);
            SetOtpBusy(false);
            await CompleteMutationAsync();
        }
    }

    private async void VerifyButton_Click(object sender, RoutedEventArgs e)
    {
        var code = string.Concat(_otpBoxes.Select(box => box.Text));
        if (code.Length != 6 || code.Any(character => character is < '0' or > '9'))
        {
            return;
        }

        _mutationInProgress = true;
        var token = BeginOperation();
        SetOtpBusy(true);
        VerifyButton.Content = "Verifying…";
        HideInlineError(OtpErrorBorder);

        try
        {
            var verifyResponse = await _pipeClient.VerifyOtpAsync(code, token);
            EnsureSuccess(verifyResponse, "The access code could not be verified.");
            ClearOtp();

            ShowConnecting("Connecting…", "Creating a secure WireZTNA session.");
            await ConnectAndConfirmAsync(token);
        }
        catch (OperationCanceledException) when (token.IsCancellationRequested)
        {
            // Superseded or closing.
        }
        catch (Exception ex) when (IsExpectedOperationException(ex))
        {
            if (OtpView.Visibility == Visibility.Visible)
            {
                ClearOtp();
                ShowInlineError(OtpErrorBorder, OtpErrorText, ex.Message);
            }
            else
            {
                ShowFatalError(ex.Message, RetryConnectAsync);
            }
        }
        finally
        {
            VerifyButton.Content = "Verify code";
            SetOtpBusy(false);
            await CompleteMutationAsync();
        }
    }

    private async Task RetryConnectAsync()
    {
        _mutationInProgress = true;
        var token = BeginOperation();
        try
        {
            await ConnectAndConfirmAsync(token);
        }
        catch (OperationCanceledException) when (token.IsCancellationRequested)
        {
            // Superseded or closing.
        }
        catch (Exception ex) when (IsExpectedOperationException(ex))
        {
            ShowFatalError(ex.Message, RetryConnectAsync);
        }
        finally
        {
            await CompleteMutationAsync();
        }
    }

    private async Task WaitForExistingConnectionAsync(CancellationToken cancellationToken)
    {
        ShowConnecting("Connecting…", "Waiting for the WireZTNA service to finish connecting.");
        using var waitTimeout = new CancellationTokenSource(TimeSpan.FromSeconds(35));
        using var linkedCancellation = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken, waitTimeout.Token);

        try
        {
            while (true)
            {
                await Task.Delay(500, linkedCancellation.Token);
                var statusResponse = await _pipeClient.GetStatusAsync(linkedCancellation.Token);
                var state = EnsureSuccess(statusResponse, "Unable to confirm the WireZTNA connection.");

                if (string.Equals(state?.Status, "connected", StringComparison.OrdinalIgnoreCase))
                {
                    ShowSuccess();
                    return;
                }

                if (!IsTransitionalStatus(state?.Status))
                {
                    throw new WizardOperationException(state?.ErrorMessage ?? "WireZTNA did not reach the connected state.");
                }
            }
        }
        catch (OperationCanceledException) when (waitTimeout.IsCancellationRequested && !cancellationToken.IsCancellationRequested)
        {
            throw new WizardOperationException("WireZTNA did not finish connecting in time.");
        }
    }

    private static bool IsTransitionalStatus(string? status) =>
        string.Equals(status, "connecting", StringComparison.OrdinalIgnoreCase) ||
        string.Equals(status, "reconnecting", StringComparison.OrdinalIgnoreCase);

    private async Task ConnectAndConfirmAsync(CancellationToken cancellationToken)
    {
        ShowConnecting("Connecting…", "Creating a secure WireZTNA session.");
        var connectResponse = await _pipeClient.ConnectAsync(cancellationToken);

        // Another local controller may win the race between our status check and
        // connect command. In that one case, observe the in-flight connection.
        if (!connectResponse.Success &&
            connectResponse.Error?.StartsWith("connection in progress", StringComparison.OrdinalIgnoreCase) == true)
        {
            var racedStatusResponse = await _pipeClient.GetStatusAsync(cancellationToken);
            var racedState = EnsureSuccess(racedStatusResponse, "Unable to confirm the WireZTNA connection.");
            if (IsTransitionalStatus(racedState?.Status))
            {
                await WaitForExistingConnectionAsync(cancellationToken);
                return;
            }
            if (string.Equals(racedState?.Status, "connected", StringComparison.OrdinalIgnoreCase))
            {
                ShowSuccess();
                return;
            }
        }

        var connectedState = EnsureSuccess(connectResponse, "WireZTNA could not connect.");

        if (!string.Equals(connectedState?.Status, "connected", StringComparison.OrdinalIgnoreCase))
        {
            var statusResponse = await _pipeClient.GetStatusAsync(cancellationToken);
            connectedState = EnsureSuccess(statusResponse, "Unable to confirm the WireZTNA connection.");
        }

        if (IsTransitionalStatus(connectedState?.Status))
        {
            await WaitForExistingConnectionAsync(cancellationToken);
            return;
        }

        if (!string.Equals(connectedState?.Status, "connected", StringComparison.OrdinalIgnoreCase))
        {
            throw new WizardOperationException(connectedState?.ErrorMessage ?? "WireZTNA did not reach the connected state.");
        }

        ShowSuccess();
    }

    private static ServiceState? EnsureSuccess(IpcResponse response, string fallbackMessage)
    {
        if (!response.Success)
        {
            throw new WizardOperationException(string.IsNullOrWhiteSpace(response.Error) ? fallbackMessage : response.Error);
        }

        return response.Data;
    }

    private static bool IsExpectedOperationException(Exception exception) =>
        exception is WireZtnaIpcException or WizardOperationException;

    private void ConfigureOtpInputs()
    {
        _otpBoxes = [OtpDigit1, OtpDigit2, OtpDigit3, OtpDigit4, OtpDigit5, OtpDigit6];
        for (var index = 0; index < _otpBoxes.Length; index++)
        {
            var box = _otpBoxes[index];
            box.Tag = index;
            box.PreviewTextInput += OtpBox_PreviewTextInput;
            box.PreviewKeyDown += OtpBox_PreviewKeyDown;
            box.TextChanged += OtpBox_TextChanged;
            DataObject.AddPastingHandler(box, OtpBox_OnPaste);
        }
    }

    private static void OtpBox_PreviewTextInput(object sender, TextCompositionEventArgs e)
    {
        e.Handled = e.Text.Length != 1 || e.Text[0] is < '0' or > '9';
    }

    private void OtpBox_TextChanged(object sender, TextChangedEventArgs e)
    {
        if (_settingOtpText || sender is not TextBox box)
        {
            return;
        }

        var sanitized = new string(box.Text.Where(character => character is >= '0' and <= '9').Take(1).ToArray());
        if (box.Text != sanitized)
        {
            _settingOtpText = true;
            box.Text = sanitized;
            box.CaretIndex = box.Text.Length;
            _settingOtpText = false;
        }

        box.BorderBrush = string.IsNullOrEmpty(box.Text) ? DefaultOtpBorderBrush : FilledOtpBorderBrush;
        if (box.Text.Length == 1 && box.Tag is int index && index < _otpBoxes.Length - 1)
        {
            _otpBoxes[index + 1].Focus();
            _otpBoxes[index + 1].SelectAll();
        }

        UpdateVerifyButton();
    }

    private void OtpBox_PreviewKeyDown(object sender, KeyEventArgs e)
    {
        if (sender is not TextBox box || box.Tag is not int index)
        {
            return;
        }

        if (e.Key == Key.Back && box.Text.Length == 0 && index > 0)
        {
            _otpBoxes[index - 1].Focus();
            _otpBoxes[index - 1].SelectAll();
            e.Handled = true;
        }
        else if (e.Key == Key.Left && index > 0)
        {
            _otpBoxes[index - 1].Focus();
            e.Handled = true;
        }
        else if (e.Key == Key.Right && index < _otpBoxes.Length - 1)
        {
            _otpBoxes[index + 1].Focus();
            e.Handled = true;
        }
    }

    private void OtpBox_OnPaste(object sender, DataObjectPastingEventArgs e)
    {
        if (sender is not TextBox box || box.Tag is not int startIndex || !e.DataObject.GetDataPresent(DataFormats.UnicodeText))
        {
            e.CancelCommand();
            return;
        }

        var text = e.DataObject.GetData(DataFormats.UnicodeText) as string ?? string.Empty;
        var digits = text.Where(character => character is >= '0' and <= '9').Take(_otpBoxes.Length - startIndex).ToArray();
        if (digits.Length == 0)
        {
            e.CancelCommand();
            return;
        }

        _settingOtpText = true;
        for (var offset = 0; offset < digits.Length; offset++)
        {
            var target = _otpBoxes[startIndex + offset];
            target.Text = digits[offset].ToString();
            target.BorderBrush = FilledOtpBorderBrush;
        }
        _settingOtpText = false;

        var focusIndex = Math.Min(startIndex + digits.Length, _otpBoxes.Length - 1);
        _otpBoxes[focusIndex].Focus();
        _otpBoxes[focusIndex].CaretIndex = _otpBoxes[focusIndex].Text.Length;
        UpdateVerifyButton();
        e.CancelCommand();
    }

    private void UpdateVerifyButton()
    {
        VerifyButton.IsEnabled = _otpBoxes.All(box => box.Text.Length == 1) && !_otpBoxes.Any(box => !box.IsEnabled);
    }

    private void ClearOtp()
    {
        _settingOtpText = true;
        foreach (var box in _otpBoxes)
        {
            box.Clear();
            box.BorderBrush = DefaultOtpBorderBrush;
        }
        _settingOtpText = false;
        UpdateVerifyButton();
        Dispatcher.BeginInvoke(() => OtpDigit1.Focus(), DispatcherPriority.Input);
    }

    private void ShowEnroll()
    {
        ShowOnly(EnrollView);
        HideInlineError(EnrollErrorBorder);
        SetEnrollBusy(false);
        Dispatcher.BeginInvoke(() => EnrollmentUrlBox.Focus(), DispatcherPriority.Input);
    }

    private void ShowLogin(string? maskedEmail)
    {
        _maskedEmail = maskedEmail ?? _maskedEmail;
        LoginDescriptionText.Text = string.IsNullOrWhiteSpace(_maskedEmail)
            ? "A 6-digit access code will be sent to your registered email."
            : $"A 6-digit access code will be sent to {_maskedEmail}.";
        ShowOnly(LoginView);
        HideInlineError(LoginErrorBorder);
        SetLoginBusy(false);
        Dispatcher.BeginInvoke(() => LoginButton.Focus(), DispatcherPriority.Input);
    }

    private void ShowOtp(string? maskedEmail)
    {
        _maskedEmail = maskedEmail ?? _maskedEmail;
        OtpEmailRun.Text = string.IsNullOrWhiteSpace(_maskedEmail) ? "your registered email" : _maskedEmail;
        ShowOnly(OtpView);
        HideInlineError(OtpErrorBorder);
        ClearOtp();
    }

    private void ShowConnecting(string title, string description)
    {
        ConnectingTitleText.Text = title;
        ConnectingDescriptionText.Text = description;
        ShowOnly(ConnectingView);
        Dispatcher.BeginInvoke(() =>
        {
            ConnectingTitleText.Focus();
            RaiseLiveRegionChanged(ConnectingTitleText);
        }, DispatcherPriority.Input);
    }

    private void ShowDisconnected(string? errorMessage)
    {
        DisconnectedDescriptionText.Text = string.IsNullOrWhiteSpace(errorMessage)
            ? "WireZTNA is ready. Use the tray or TUI to connect and choose Split Tunnel or VPN mode."
            : $"The tunnel is not connected: {errorMessage} Use the tray or TUI to retry without changing the selected mode.";
        ShowOnly(DisconnectedView);
        Dispatcher.BeginInvoke(() =>
        {
            RaiseLiveRegionChanged(DisconnectedTitleText);
            DisconnectedCloseButton.Focus();
        }, DispatcherPriority.Input);
    }

    private void ShowSuccess()
    {
        ShowOnly(SuccessView);
        Dispatcher.BeginInvoke(() =>
        {
            RaiseLiveRegionChanged(SuccessTitleText);
            SuccessCloseButton.Focus();
        }, DispatcherPriority.Input);
        System.Media.SystemSounds.Asterisk.Play();
    }

    private void ShowFatalError(string message, Func<Task> retryAction)
    {
        _retryAction = retryAction;
        FatalErrorText.Text = message;
        ShowOnly(ErrorView);
        Dispatcher.BeginInvoke(() =>
        {
            RaiseLiveRegionChanged(FatalErrorText);
            RetryButton.Focus();
        }, DispatcherPriority.Input);
    }

    private async void RetryButton_Click(object sender, RoutedEventArgs e)
    {
        var retry = _retryAction;
        if (retry is not null)
        {
            await retry();
        }
    }

    private void ShowOnly(UIElement visibleView)
    {
        foreach (var view in new UIElement[] { EnrollView, LoginView, OtpView, ConnectingView, DisconnectedView, SuccessView, ErrorView })
        {
            view.Visibility = ReferenceEquals(view, visibleView) ? Visibility.Visible : Visibility.Collapsed;
        }
    }

    private static void ShowInlineError(Border border, TextBlock textBlock, string message)
    {
        textBlock.Text = message;
        border.Visibility = Visibility.Visible;
        RaiseLiveRegionChanged(textBlock);
    }

    private static void RaiseLiveRegionChanged(UIElement element)
    {
        if (!AutomationPeer.ListenerExists(AutomationEvents.LiveRegionChanged))
        {
            return;
        }

        var peer = UIElementAutomationPeer.FromElement(element) ?? UIElementAutomationPeer.CreatePeerForElement(element);
        peer?.RaiseAutomationEvent(AutomationEvents.LiveRegionChanged);
    }

    private static void HideInlineError(Border border)
    {
        border.Visibility = Visibility.Collapsed;
    }

    private void SetEnrollBusy(bool busy)
    {
        EnrollmentUrlBox.IsEnabled = !busy;
        EnrollButton.IsEnabled = !busy;
        EnrollButton.Content = busy ? "Enrolling…" : "Enroll this device";
    }

    private void SetLoginBusy(bool busy)
    {
        LoginButton.IsEnabled = !busy;
        LoginButton.Content = busy ? "Sending…" : "Send access code";
    }

    private void SetOtpBusy(bool busy)
    {
        foreach (var box in _otpBoxes)
        {
            box.IsEnabled = !busy;
        }
        ResendButton.IsEnabled = !busy;
        VerifyButton.IsEnabled = !busy && _otpBoxes.All(box => box.Text.Length == 1);
    }

    private async Task CompleteMutationAsync()
    {
        _mutationInProgress = false;
        if (!_hasPendingActivation || _lifetimeCancellation.IsCancellationRequested)
        {
            return;
        }

        var requestedStep = _pendingActivationStep;
        _pendingActivationStep = null;
        _hasPendingActivation = false;
        await ReconcileWithServiceAsync(requestedStep);
    }

    private CancellationToken BeginOperation()
    {
        var replacement = CancellationTokenSource.CreateLinkedTokenSource(_lifetimeCancellation.Token);
        var previous = Interlocked.Exchange(ref _activeOperation, replacement);
        if (previous is not null)
        {
            previous.Cancel();
            previous.Dispose();
        }
        return replacement.Token;
    }

    private static string GetDisplayVersion()
    {
        var version = Assembly.GetExecutingAssembly().GetName().Version;
        return version is null ? "0.9.29" : $"{version.Major}.{version.Minor}.{version.Build}";
    }

    private void Window_PreviewKeyDown(object sender, KeyEventArgs e)
    {
        if (e.Key == Key.Escape)
        {
            Close();
            e.Handled = true;
        }
    }

    private void CloseButton_Click(object sender, RoutedEventArgs e) => Close();

    private void Window_Closing(object? sender, CancelEventArgs e)
    {
        _lifetimeCancellation.Cancel();
        _activeOperation?.Cancel();
        _activeOperation?.Dispose();
        _activeOperation = null;
        _lifetimeCancellation.Dispose();
    }

    private sealed class WizardOperationException(string message) : Exception(message);
}
