using System.Text.Json.Serialization;

namespace WireZTNA.Auth.Models;

internal sealed class IpcRequest
{
    [JsonPropertyName("command")]
    public required string Command { get; init; }

    [JsonPropertyName("enroll_url")]
    [JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    public string? EnrollUrl { get; init; }

    [JsonPropertyName("otp_code")]
    [JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    public string? OtpCode { get; init; }
}

internal sealed class IpcResponse
{
    [JsonPropertyName("success")]
    public bool Success { get; init; }

    [JsonPropertyName("error")]
    public string? Error { get; init; }

    [JsonPropertyName("data")]
    public ServiceState? Data { get; init; }
}

internal sealed class ServiceState
{
    [JsonPropertyName("status")]
    public string? Status { get; init; }

    [JsonPropertyName("overlay_ip")]
    public string? OverlayIp { get; init; }

    [JsonPropertyName("error_message")]
    public string? ErrorMessage { get; init; }

    [JsonPropertyName("needs_enroll")]
    public bool NeedsEnroll { get; init; }

    [JsonPropertyName("needs_login")]
    public bool NeedsLogin { get; init; }

    [JsonPropertyName("email")]
    public string? Email { get; init; }
}
