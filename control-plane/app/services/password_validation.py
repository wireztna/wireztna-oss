"""Password validation service — compliance control A.5.17 / CC6.1.

Enforces minimum password complexity requirements:
- Minimum 12 characters
- Not in a list of commonly breached passwords
"""

# Top 50 most common passwords (subset of known breach lists).
# This list prevents the worst offenders without requiring an external database.
COMMON_PASSWORDS = frozenset({
    "password", "123456", "12345678", "123456789", "1234567890",
    "qwerty", "abc123", "password1", "password123", "admin",
    "letmein", "welcome", "monkey", "dragon", "master",
    "login", "princess", "football", "shadow", "sunshine",
    "trustno1", "iloveyou", "batman", "access", "hello",
    "charlie", "donald", "passw0rd", "qwerty123", "654321",
    "superman", "michael", "ashley", "jessica", "121212",
    "password1234", "administrator", "changeme", "welcome1",
    "p@ssw0rd", "p@ssword", "pass1234", "test1234", "guest",
    "root", "toor", "default", "secret", "1q2w3e4r",
})

MIN_PASSWORD_LENGTH = 12


def validate_password(password: str) -> tuple[bool, str | None]:
    """Validate a password against complexity requirements.

    Returns:
        (True, None) if valid.
        (False, error_message) if invalid.
    """
    if not password or len(password) < MIN_PASSWORD_LENGTH:
        return False, f"Password must be at least {MIN_PASSWORD_LENGTH} characters"

    if password.lower() in COMMON_PASSWORDS:
        return False, "This password is too common — please choose a stronger one"

    return True, None
