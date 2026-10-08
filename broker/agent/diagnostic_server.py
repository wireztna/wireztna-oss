"""Lightweight HTTP server exposing broker diagnostics on demand.

Runs in a background thread alongside the reconciler. Listens on a configurable
port (default 8080) and exposes:

  GET /diagnose        — Runs diagnose.sh --json and returns the result
  GET /diagnose?quick  — Runs diagnose.sh --quick --json (faster, no pings)
  GET /health          — Simple liveness check for the agent itself

Security: This server is intended to be accessed only by the control plane
over the internal network (not exposed publicly). The broker runs on host
network mode, so bind to 127.0.0.1 by default or 0.0.0.0 if the control
plane is on a different host (controlled by DIAG_BIND_HOST).
"""

import json
import logging
import os
import subprocess
import threading
from http.server import HTTPServer, BaseHTTPRequestHandler
from pathlib import Path

logger = logging.getLogger("broker-diag-server")

DIAG_PORT = int(os.environ.get("BROKER_DIAG_PORT", "8080"))
DIAG_BIND_HOST = os.environ.get("BROKER_DIAG_HOST", "0.0.0.0")
SCRIPTS_DIR = Path(os.environ.get("SCRIPTS_DIR", "/opt/wireztna/scripts"))
DIAGNOSE_SCRIPT = SCRIPTS_DIR / "diagnose.sh"


class DiagnosticHandler(BaseHTTPRequestHandler):
    """Handles diagnostic HTTP requests."""

    def log_message(self, format, *args):
        """Route access logs through the standard logger."""
        logger.debug(f"HTTP {args[0]}")

    def do_GET(self):
        if self.path == "/health":
            self._respond_json(200, {"status": "running", "service": "broker-agent"})

        elif self.path.startswith("/diagnose"):
            self._run_diagnostic()

        else:
            self._respond_json(404, {"error": "Not found. Use /diagnose or /health"})

    def _run_diagnostic(self):
        """Execute diagnose.sh --json and return its output."""
        if not DIAGNOSE_SCRIPT.exists():
            self._respond_json(500, {
                "error": "diagnose.sh not found",
                "detail": f"Expected at {DIAGNOSE_SCRIPT}",
            })
            return

        # Build command
        cmd = ["bash", str(DIAGNOSE_SCRIPT), "--json"]
        if "quick" in self.path:
            cmd.append("--quick")

        # Optional: focus on a specific publisher or client via query params
        # /diagnose?publisher=a1b2c3d4 or /diagnose?client=10.200.0.15
        if "publisher=" in self.path:
            pub_id = self.path.split("publisher=")[1].split("&")[0]
            cmd.extend(["--publisher", pub_id])
        elif "client=" in self.path:
            client_ip = self.path.split("client=")[1].split("&")[0]
            cmd.extend(["--client", client_ip])

        try:
            result = subprocess.run(
                cmd,
                capture_output=True,
                text=True,
                timeout=30,  # Hard timeout to prevent hanging
            )

            # diagnose.sh returns JSON on stdout; exit code indicates severity
            output = result.stdout.strip()
            if output:
                try:
                    diagnostic_data = json.loads(output)
                    # Add metadata about the execution
                    diagnostic_data["exit_code"] = result.returncode
                    diagnostic_data["execution"] = "success"
                    self._respond_json(200, diagnostic_data)
                except json.JSONDecodeError:
                    # Script produced non-JSON output (shouldn't happen with --json)
                    self._respond_json(200, {
                        "execution": "partial",
                        "exit_code": result.returncode,
                        "raw_output": output,
                        "stderr": result.stderr.strip() if result.stderr else None,
                    })
            else:
                self._respond_json(500, {
                    "execution": "failed",
                    "exit_code": result.returncode,
                    "stderr": result.stderr.strip() if result.stderr else "No output",
                })

        except subprocess.TimeoutExpired:
            self._respond_json(504, {
                "execution": "timeout",
                "detail": "Diagnostic script timed out after 30s",
            })
        except OSError as e:
            self._respond_json(500, {
                "execution": "error",
                "detail": str(e),
            })

    def _respond_json(self, status_code: int, data: dict):
        """Send a JSON response."""
        body = json.dumps(data, indent=2, default=str).encode()
        self.send_response(status_code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def start_diagnostic_server():
    """Start the diagnostic HTTP server in a daemon thread.

    Call this from the reconciler's main() before entering the poll loop.
    The thread is a daemon so it dies when the main process exits.
    """
    server = HTTPServer((DIAG_BIND_HOST, DIAG_PORT), DiagnosticHandler)

    thread = threading.Thread(
        target=server.serve_forever,
        name="diag-server",
        daemon=True,
    )
    thread.start()
    logger.info(f"Diagnostic server listening on {DIAG_BIND_HOST}:{DIAG_PORT}")
    return server
