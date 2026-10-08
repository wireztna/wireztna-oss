"""TCP namespace bridge helper for WebSocket tunnel relay.

Opens a TCP connection inside a publisher's network namespace using
`ip netns exec` and a Python subprocess that acts as a stdio↔TCP bridge.

Requirements: 10.1, 10.2
"""

import asyncio
import logging

logger = logging.getLogger(__name__)


class TunnelError(Exception):
    """Base exception for tunnel-related errors."""
    pass


class TargetUnreachableError(TunnelError):
    """Target host:port is unreachable from the namespace."""
    pass


class NamespaceNotFoundError(TunnelError):
    """The network namespace does not exist."""
    pass


# Python one-liner that connects TCP and relays stdin↔socket bidirectionally.
# This runs inside the network namespace via `ip netns exec`.
_TCP_BRIDGE_SCRIPT = '''
import socket, sys, select, os

sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
sock.settimeout({timeout})
try:
    sock.connect(("{host}", {port}))
except (socket.timeout, ConnectionRefusedError, OSError) as e:
    sys.stderr.write(f"CONNECT_FAILED:{{e}}\\n")
    sys.exit(1)

sock.setblocking(False)
stdin_fd = sys.stdin.buffer.fileno()
os.set_blocking(stdin_fd, False)
stdout = sys.stdout.buffer

try:
    while True:
        readable, _, _ = select.select([sock, stdin_fd], [], [], 1.0)
        for r in readable:
            if r is sock:
                data = sock.recv(65536)
                if not data:
                    sys.exit(0)
                stdout.write(data)
                stdout.flush()
            elif r == stdin_fd:
                data = os.read(stdin_fd, 65536)
                if not data:
                    sys.exit(0)
                sock.sendall(data)
except (BrokenPipeError, ConnectionResetError, OSError):
    pass
finally:
    sock.close()
'''


async def open_tcp_in_namespace(
    namespace: str,
    host: str,
    port: int,
    timeout: float = 10.0,
) -> asyncio.subprocess.Process:
    """Open a TCP connection inside a publisher namespace using subprocess.

    Launches `ip netns exec <namespace> python3 -c <bridge_script>` which:
    1. Connects TCP to host:port inside the namespace
    2. Relays stdin→TCP and TCP→stdout bidirectionally

    Args:
        namespace: Linux network namespace name (e.g., "ns-abcdef12")
        host: Target IP address to connect to
        port: Target TCP port
        timeout: TCP connection timeout in seconds

    Returns:
        asyncio.subprocess.Process with stdin/stdout pipes for relay.

    Raises:
        NamespaceNotFoundError: If the namespace doesn't exist.
        TargetUnreachableError: If TCP connection to host:port fails.
    """
    script = _TCP_BRIDGE_SCRIPT.format(host=host, port=port, timeout=timeout)

    try:
        proc = await asyncio.create_subprocess_exec(
            "ip", "netns", "exec", namespace,
            "python3", "-c", script,
            stdin=asyncio.subprocess.PIPE,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
    except FileNotFoundError:
        raise NamespaceNotFoundError(
            f"Cannot execute 'ip netns exec': command not found"
        )

    # Wait a short time to check if the process immediately fails
    # (namespace not found, python3 not found, connection refused)
    try:
        await asyncio.wait_for(_check_process_started(proc), timeout=timeout + 2)
    except asyncio.TimeoutError:
        # Process is still running — that's good, it connected
        pass

    return proc


async def _check_process_started(proc: asyncio.subprocess.Process) -> None:
    """Check if the bridge subprocess started successfully.

    Waits briefly to see if the process exits immediately (indicating failure).
    If it's still running after a short delay, it means TCP connected.
    """
    # Give the process a moment to fail if it's going to
    await asyncio.sleep(0.3)

    if proc.returncode is not None:
        # Process already exited — read stderr for the error
        stderr_data = b""
        if proc.stderr:
            stderr_data = await proc.stderr.read()
        stderr_text = stderr_data.decode(errors="replace").strip()

        if "CONNECT_FAILED" in stderr_text:
            raise TargetUnreachableError(
                f"TCP connection failed: {stderr_text}"
            )
        elif "No such file or directory" in stderr_text or "does not exist" in stderr_text:
            raise NamespaceNotFoundError(
                f"Namespace error: {stderr_text}"
            )
        else:
            raise TunnelError(
                f"Bridge subprocess failed (exit {proc.returncode}): {stderr_text}"
            )

    # Process is still running — TCP connected successfully
    # Raise TimeoutError to signal the caller that we're good
    raise asyncio.TimeoutError()
