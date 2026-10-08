"""Service utilities for delegated access passes."""

import secrets
import string


def generate_pass_id() -> str:
    """Generate a unique pass ID: dap_ + 6 random alphanumeric chars."""
    chars = string.ascii_lowercase + string.digits
    suffix = ''.join(secrets.choice(chars) for _ in range(6))
    return f"dap_{suffix}"
