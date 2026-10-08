using System.IO;
using System.IO.Pipes;
using System.Text;
using System.Text.Json;
using WireZTNA.Auth.Models;

namespace WireZTNA.Auth.Infrastructure;

internal sealed class WireZtnaPipeClient
{
    private const string PipeName = "wireztna";
    private static readonly UTF8Encoding Utf8WithoutBom = new(false);
    private static readonly JsonSerializerOptions JsonOptions = new(JsonSerializerDefaults.Web);

    public Task<IpcResponse> GetStatusAsync(CancellationToken cancellationToken) =>
        SendAsync(new IpcRequest { Command = "status" }, TimeSpan.FromSeconds(10), cancellationToken);

    public Task<IpcResponse> EnrollAsync(string enrollmentUrl, CancellationToken cancellationToken) =>
        SendAsync(new IpcRequest { Command = "enroll", EnrollUrl = enrollmentUrl }, TimeSpan.FromSeconds(35), cancellationToken);

    public Task<IpcResponse> LoginAsync(CancellationToken cancellationToken) =>
        SendAsync(new IpcRequest { Command = "login" }, TimeSpan.FromSeconds(35), cancellationToken);

    public Task<IpcResponse> VerifyOtpAsync(string code, CancellationToken cancellationToken) =>
        SendAsync(new IpcRequest { Command = "otp_verify", OtpCode = code }, TimeSpan.FromSeconds(35), cancellationToken);

    public Task<IpcResponse> ConnectAsync(CancellationToken cancellationToken) =>
        SendAsync(new IpcRequest { Command = "connect" }, TimeSpan.FromSeconds(35), cancellationToken);

    private static async Task<IpcResponse> SendAsync(IpcRequest request, TimeSpan timeout, CancellationToken cancellationToken)
    {
        using var timeoutSource = new CancellationTokenSource(timeout);
        using var linkedSource = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken, timeoutSource.Token);
        var token = linkedSource.Token;

        try
        {
            await using var pipe = new NamedPipeClientStream(
                ".",
                PipeName,
                PipeDirection.InOut,
                PipeOptions.Asynchronous);

            await pipe.ConnectAsync(token).ConfigureAwait(false);

            await using var writer = new StreamWriter(pipe, Utf8WithoutBom, bufferSize: 1024, leaveOpen: true)
            {
                NewLine = "\n",
                AutoFlush = false
            };
            using var reader = new StreamReader(pipe, Utf8WithoutBom, detectEncodingFromByteOrderMarks: false, bufferSize: 4096, leaveOpen: true);

            var payload = JsonSerializer.Serialize(request, JsonOptions);
            await writer.WriteLineAsync(payload.AsMemory(), token).ConfigureAwait(false);
            await writer.FlushAsync(token).ConfigureAwait(false);

            var responseLine = await reader.ReadLineAsync(token).ConfigureAwait(false);
            if (string.IsNullOrWhiteSpace(responseLine))
            {
                throw new WireZtnaIpcException("The WireZTNA service returned an empty response.");
            }

            IpcResponse? response;
            try
            {
                response = JsonSerializer.Deserialize<IpcResponse>(responseLine, JsonOptions);
            }
            catch (JsonException ex)
            {
                throw new WireZtnaIpcException("The WireZTNA service returned an invalid response.", ex);
            }

            return response ?? throw new WireZtnaIpcException("The WireZTNA service returned an invalid response.");
        }
        catch (OperationCanceledException) when (timeoutSource.IsCancellationRequested && !cancellationToken.IsCancellationRequested)
        {
            throw new WireZtnaIpcException("The WireZTNA service did not respond in time.");
        }
        catch (IOException ex)
        {
            throw new WireZtnaIpcException("Cannot communicate with the WireZTNA service. Make sure the service is running.", ex);
        }
        catch (UnauthorizedAccessException ex)
        {
            throw new WireZtnaIpcException("Access to the WireZTNA service was denied.", ex);
        }
    }
}

internal sealed class WireZtnaIpcException : Exception
{
    public WireZtnaIpcException(string message) : base(message) { }
    public WireZtnaIpcException(string message, Exception innerException) : base(message, innerException) { }
}
