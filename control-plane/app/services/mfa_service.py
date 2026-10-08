"""MFA service — backup codes and enforcement logic (A.8.5 / CC6.1)."""

import hashlib
import json
import secrets

BACKUP_CODE_COUNT = 8
BACKUP_CODE_LENGTH = 8  # 8 alphanumeric chars per code


def generate_backup_codes() -> tuple[list[str], str]:
    """Generate backup codes and their hashed storage form.

    Returns:
        (plaintext_codes, hashed_json) — show plaintext_codes to user once,
        store hashed_json in user.mfa_backup_codes.
    """
    codes = [secrets.token_hex(BACKUP_CODE_LENGTH // 2) for _ in range(BACKUP_CODE_COUNT)]
    hashed = [hashlib.sha256(c.encode()).hexdigest() for c in codes]
    return codes, json.dumps(hashed)


def verify_backup_code(code: str, hashed_json: str | None) -> tuple[bool, str | None]:
    """Verify a backup code against stored hashes. Returns (valid, updated_hashed_json).

    If valid, the used code is removed from the list (single-use).
    """
    if not hashed_json:
        return False, hashed_json

    try:
        hashes = json.loads(hashed_json)
    except (json.JSONDecodeError, TypeError):
        return False, hashed_json

    code_hash = hashlib.sha256(code.strip().encode()).hexdigest()

    if code_hash in hashes:
        hashes.remove(code_hash)
        return True, json.dumps(hashes)

    return False, hashed_json


def check_mfa_enforcement(user_role: str, org_mfa_required: bool = False) -> bool:
    """Check if MFA is required for this user based on role and org policy.

    MFA is mandatory for:
    - super_admin (always)
    - org_admin (always)
    - any user in an org where mfa_required=True
    """
    if user_role in ("super_admin", "org_admin"):
        return True
    return org_mfa_required
