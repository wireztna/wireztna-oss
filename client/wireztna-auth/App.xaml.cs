using System.Diagnostics;
using System.Windows;
using WireZTNA.Auth.Infrastructure;

namespace WireZTNA.Auth;

public partial class App : Application
{
    private const int ActivationForwardedExitCode = 20;
    private const int ActivationFailedExitCode = 21;

    private readonly CancellationTokenSource _lifetimeCancellation = new();
    private CancellationTokenSource? _parentMonitorCancellation;
    private SingleInstanceCoordinator? _singleInstance;
    private MainWindow? _mainWindow;

    protected override async void OnStartup(StartupEventArgs e)
    {
        base.OnStartup(e);

        var options = LaunchOptions.Parse(e.Args);
        _singleInstance = new SingleInstanceCoordinator();

        if (!_singleInstance.IsPrimaryInstance)
        {
            var activationForwarded = false;
            try
            {
                using var activationTimeout = new CancellationTokenSource(TimeSpan.FromSeconds(4));
                activationForwarded = await _singleInstance.ActivatePrimaryAsync(
                    options.Step,
                    options.ParentProcessId,
                    activationTimeout.Token);
            }
            catch (OperationCanceledException)
            {
                // Report a failed handoff so the tray can open its compatibility fallback.
            }

            Shutdown(activationForwarded ? ActivationForwardedExitCode : ActivationFailedExitCode);
            return;
        }

        _mainWindow = new MainWindow(options.Step);
        MainWindow = _mainWindow;

        _singleInstance.ActivationRequested += (requestedStep, parentProcessId) =>
        {
            Dispatcher.BeginInvoke(async () =>
            {
                if (parentProcessId is int activatedParentProcessId)
                {
                    StartParentMonitor(activatedParentProcessId);
                }

                if (_mainWindow is not null)
                {
                    await _mainWindow.ActivateFromAnotherInstanceAsync(requestedStep);
                }
            });
        };
        _singleInstance.StartListening();

        _mainWindow.Show();

        if (options.ParentProcessId is int parentProcessId)
        {
            StartParentMonitor(parentProcessId);
        }
    }

    private void StartParentMonitor(int parentProcessId)
    {
        var replacement = CancellationTokenSource.CreateLinkedTokenSource(_lifetimeCancellation.Token);
        var previous = Interlocked.Exchange(ref _parentMonitorCancellation, replacement);
        if (previous is not null)
        {
            previous.Cancel();
            previous.Dispose();
        }

        _ = MonitorParentAsync(parentProcessId, replacement.Token);
    }

    private async Task MonitorParentAsync(int parentProcessId, CancellationToken cancellationToken)
    {
        try
        {
            using var parent = Process.GetProcessById(parentProcessId);
            await parent.WaitForExitAsync(cancellationToken).ConfigureAwait(false);
            await Dispatcher.InvokeAsync(Shutdown);
        }
        catch (ArgumentException)
        {
            if (!cancellationToken.IsCancellationRequested)
            {
                // A requested parent that already exited means the wizard is no longer needed.
                await Dispatcher.InvokeAsync(Shutdown);
            }
        }
        catch (InvalidOperationException)
        {
            if (!cancellationToken.IsCancellationRequested)
            {
                await Dispatcher.InvokeAsync(Shutdown);
            }
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
        {
            // Normal application shutdown.
        }
    }

    protected override void OnExit(ExitEventArgs e)
    {
        _lifetimeCancellation.Cancel();
        _parentMonitorCancellation?.Cancel();
        _parentMonitorCancellation?.Dispose();
        _parentMonitorCancellation = null;
        _lifetimeCancellation.Dispose();

        if (_singleInstance is not null)
        {
            _singleInstance.DisposeAsync().AsTask().GetAwaiter().GetResult();
        }

        base.OnExit(e);
    }

    private sealed record LaunchOptions(string? Step, int? ParentProcessId)
    {
        public static LaunchOptions Parse(string[] args)
        {
            string? step = null;
            int? parentProcessId = null;

            for (var index = 0; index < args.Length; index++)
            {
                if (args[index].Equals("--step", StringComparison.OrdinalIgnoreCase) && index + 1 < args.Length)
                {
                    step = SingleInstanceCoordinator.NormalizeStep(args[++index]);
                }
                else if (args[index].Equals("--parent-pid", StringComparison.OrdinalIgnoreCase) &&
                         index + 1 < args.Length &&
                         int.TryParse(args[++index], out var parsedPid) &&
                         parsedPid > 0)
                {
                    parentProcessId = parsedPid;
                }
            }

            return new LaunchOptions(step, parentProcessId);
        }
    }
}
