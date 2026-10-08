using System.IO;
using System.IO.Pipes;
using System.Security.Principal;
using System.Text;
using System.Text.Json;

namespace WireZTNA.Auth.Infrastructure;

internal sealed class SingleInstanceCoordinator : IAsyncDisposable
{
    private static readonly UTF8Encoding Utf8WithoutBom = new(false);
    private readonly Mutex _mutex;
    private readonly string _activationPipeName;
    private readonly CancellationTokenSource _listenerCancellation = new();
    private Task? _listenerTask;
    private bool _ownsMutex;

    public SingleInstanceCoordinator()
    {
        var sid = WindowsIdentity.GetCurrent().User?.Value ?? Environment.UserName;
        var safeSid = string.Concat(sid.Select(character => char.IsLetterOrDigit(character) || character == '-' ? character : '-'));
        _activationPipeName = $"wireztna-auth-{safeSid}";
        _mutex = new Mutex(initiallyOwned: true, $"Local\\WireZTNA-Auth-{safeSid}", out var createdNew);
        _ownsMutex = createdNew;
    }

    public bool IsPrimaryInstance => _ownsMutex;

    public event Action<string?, int?>? ActivationRequested;

    public void StartListening()
    {
        if (!IsPrimaryInstance || _listenerTask is not null)
        {
            return;
        }

        _listenerTask = ListenAsync(_listenerCancellation.Token);
    }

    public async Task<bool> ActivatePrimaryAsync(string? requestedStep, int? parentProcessId, CancellationToken cancellationToken)
    {
        var message = JsonSerializer.Serialize(new ActivationMessage(requestedStep, parentProcessId));

        for (var attempt = 0; attempt < 6; attempt++)
        {
            cancellationToken.ThrowIfCancellationRequested();
            try
            {
                await using var pipe = new NamedPipeClientStream(
                    ".",
                    _activationPipeName,
                    PipeDirection.Out,
                    PipeOptions.Asynchronous);
                using var attemptSource = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
                attemptSource.CancelAfter(TimeSpan.FromMilliseconds(500));
                await pipe.ConnectAsync(attemptSource.Token).ConfigureAwait(false);

                await using var writer = new StreamWriter(pipe, Utf8WithoutBom, bufferSize: 512, leaveOpen: true)
                {
                    NewLine = "\n"
                };
                await writer.WriteLineAsync(message.AsMemory(), cancellationToken).ConfigureAwait(false);
                await writer.FlushAsync(cancellationToken).ConfigureAwait(false);
                return true;
            }
            catch (OperationCanceledException) when (!cancellationToken.IsCancellationRequested)
            {
                // The primary instance may still be creating its activation pipe.
            }
            catch (IOException)
            {
                // Retry briefly to cover the startup race between mutex and pipe creation.
            }

            await Task.Delay(100, cancellationToken).ConfigureAwait(false);
        }

        return false;
    }

    private async Task ListenAsync(CancellationToken cancellationToken)
    {
        while (!cancellationToken.IsCancellationRequested)
        {
            try
            {
                await using var server = new NamedPipeServerStream(
                    _activationPipeName,
                    PipeDirection.In,
                    maxNumberOfServerInstances: 1,
                    PipeTransmissionMode.Byte,
                    PipeOptions.Asynchronous | PipeOptions.CurrentUserOnly);

                await server.WaitForConnectionAsync(cancellationToken).ConfigureAwait(false);
                using var reader = new StreamReader(server, Utf8WithoutBom, detectEncodingFromByteOrderMarks: false, bufferSize: 512, leaveOpen: true);
                var line = await reader.ReadLineAsync(cancellationToken).ConfigureAwait(false);
                if (string.IsNullOrWhiteSpace(line))
                {
                    continue;
                }

                var activation = JsonSerializer.Deserialize<ActivationMessage>(line);
                ActivationRequested?.Invoke(NormalizeStep(activation?.Step), activation?.ParentProcessId);
            }
            catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
            {
                break;
            }
            catch (IOException)
            {
                await DelayAfterListenerFailureAsync(cancellationToken).ConfigureAwait(false);
            }
            catch (JsonException)
            {
                // Ignore malformed local activation messages and continue listening.
            }
        }
    }

    private static async Task DelayAfterListenerFailureAsync(CancellationToken cancellationToken)
    {
        try
        {
            await Task.Delay(100, cancellationToken).ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
            // Normal shutdown.
        }
    }

    public static string? NormalizeStep(string? step) => step?.ToLowerInvariant() switch
    {
        "enroll" => "enroll",
        "login" => "login",
        _ => null
    };

    public async ValueTask DisposeAsync()
    {
        _listenerCancellation.Cancel();

        // A named Mutex is thread-affine. Release it before the first await so
        // shutdown always releases it on the UI thread that acquired it.
        if (_ownsMutex)
        {
            _mutex.ReleaseMutex();
            _ownsMutex = false;
        }

        if (_listenerTask is not null)
        {
            try
            {
                await _listenerTask.ConfigureAwait(false);
            }
            catch (OperationCanceledException)
            {
                // Normal shutdown.
            }
        }

        _listenerCancellation.Dispose();
        _mutex.Dispose();
    }

    private sealed record ActivationMessage(string? Step, int? ParentProcessId);
}
