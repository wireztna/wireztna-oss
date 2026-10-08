"""Email normalization service — prevents alias abuse in free tier registrations.

Normalizes emails to a canonical form so that:
- user+alias@gmail.com → user@gmail.com
- u.s.e.r@gmail.com → user@gmail.com
- user+tag@outlook.com → user@outlook.com
- user+anything@custom.com → user@custom.com

This prevents a single person from creating multiple free tier accounts
using the same underlying mailbox with different aliases.
"""

import re

# Providers that ignore dots in the local part (treat a.b.c as abc)
_DOT_INSENSITIVE_DOMAINS = frozenset({
    "gmail.com",
    "googlemail.com",
})

# Domain aliases (multiple domains pointing to the same mailbox service)
_DOMAIN_ALIASES = {
    "googlemail.com": "gmail.com",
}


def normalize_email(email: str) -> str:
    """Normalize an email to its canonical form.

    Steps:
    1. Lowercase and strip whitespace
    2. Map domain aliases (googlemail.com → gmail.com)
    3. Strip +suffix from local part (all providers)
    4. Remove dots from local part (Gmail/Googlemail only)

    Returns the canonical email string.
    """
    email = email.strip().lower()

    if "@" not in email:
        return email

    local, domain = email.rsplit("@", 1)

    # Map domain aliases
    domain = _DOMAIN_ALIASES.get(domain, domain)

    # Strip +alias suffix (universal — most providers support this)
    if "+" in local:
        local = local.split("+", 1)[0]

    # Remove dots for dot-insensitive providers (Gmail)
    if domain in _DOT_INSENSITIVE_DOMAINS:
        local = local.replace(".", "")

    return f"{local}@{domain}"


def has_plus_alias(email: str) -> bool:
    """Check if an email contains a +alias in the local part."""
    email = email.strip().lower()
    if "@" not in email:
        return False
    local, _ = email.rsplit("@", 1)
    return "+" in local


def validate_email_for_registration(email: str) -> "tuple[bool, str | None]":
    """Validate an email for free tier registration.

    Returns (is_valid, error_message).
    - Rejects emails with +alias syntax
    - Validates basic email format

    We reject +alias at registration time (not just normalize) because:
    - It's a clear signal of intentional abuse
    - Legitimate users don't need aliases for registration
    - Better UX to reject upfront than silently normalize
    """
    email = email.strip().lower()

    # Basic format check
    if "@" not in email or email.count("@") != 1:
        return False, "Invalid email format"

    local, domain = email.rsplit("@", 1)

    if not local or not domain or "." not in domain:
        return False, "Invalid email format"

    # Reject +alias
    if "+" in local:
        return False, "Email addresses with '+' aliases are not allowed for free tier registration"

    # Reject obviously invalid local parts
    if re.match(r'^[.]+$', local) or local.startswith(".") or local.endswith(".") or ".." in local:
        return False, "Invalid email format"

    return True, None
