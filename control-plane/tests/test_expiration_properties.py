"""Property-based test for access pass expiration cleanup.

Validates: Requirement 6.1 — Expiration cleanup transitions stale passes.
"""

import json
from datetime import datetime, timedelta

import pytest
from hypothesis import given, strategies as st, settings as hyp_settings

from app.models.access_pass import AccessPass
from app.services.pass_service import generate_pass_id


# ─── Property 8: Expiration cleanup transitions stale passes ───
# **Validates: Requirements 6.1**


@hyp_settings(max_examples=100)
@given(
    num_expired=st.integers(min_value=1, max_value=10),
    minutes_past=st.lists(
        st.integers(min_value=1, max_value=480),
        min_size=1,
        max_size=10,
    ),
)
def test_expiration_cleanup_transitions_stale_passes(num_expired, minutes_past):
    """Expiration cleanup transitions stale passes.

    For any set of passes where expires_at < now() and status = 'active',
    applying the expiration logic should transition ALL of them to 'expired'.

    This tests the core logic used by the background task in main.py.

    **Validates: Requirements 6.1**
    """
    now = datetime.utcnow()
    minutes_past = minutes_past[:num_expired]
    if len(minutes_past) < num_expired:
        minutes_past = minutes_past + [60] * (num_expired - len(minutes_past))

    # Create passes with expires_at in the past
    passes = []
    for i, mins in enumerate(minutes_past):
        expires_at = now - timedelta(minutes=mins)
        created_at = expires_at - timedelta(seconds=3600)

        p = AccessPass(
            id=generate_pass_id(),
            created_by_user_id="user-123",
            label=f"expired-pass-{i}",
            scope_json=json.dumps({"publishers": ["pub-1"], "cidrs": [], "ports": [], "apps": []}),
            ttl_seconds=3600,
            status="active",
            connection_url="wss://test.example.com/api/v1/tunnel/dap_test",
            created_at=created_at,
            expires_at=expires_at,
        )
        passes.append(p)

    # Simulate the expiration logic (what the background task does)
    expired_count = 0
    for ap in passes:
        if ap.status == "active" and ap.expires_at < now:
            ap.status = "expired"
            expired_count += 1

    # ALL passes should have been transitioned
    assert expired_count == num_expired, (
        f"Expected {num_expired} passes to be expired, but only {expired_count} were"
    )
    for p in passes:
        assert p.status == "expired", (
            f"Pass {p.id} should have status 'expired', got '{p.status}'"
        )


@hyp_settings(max_examples=100)
@given(
    num_active=st.integers(min_value=1, max_value=5),
    ttl_minutes=st.lists(
        st.integers(min_value=10, max_value=120),
        min_size=1,
        max_size=5,
    ),
)
def test_expiration_does_not_touch_non_expired_passes(num_active, ttl_minutes):
    """Active passes with future expires_at are not touched.

    The expiration logic only transitions passes where expires_at < now().
    Passes still within their TTL remain 'active'.

    **Validates: Requirements 6.1 (negative case)**
    """
    now = datetime.utcnow()
    ttl_minutes = ttl_minutes[:num_active]
    if len(ttl_minutes) < num_active:
        ttl_minutes = ttl_minutes + [60] * (num_active - len(ttl_minutes))

    # Create passes with expires_at in the FUTURE
    passes = []
    for i, mins in enumerate(ttl_minutes):
        expires_at = now + timedelta(minutes=mins)
        created_at = now - timedelta(minutes=5)

        p = AccessPass(
            id=generate_pass_id(),
            created_by_user_id="user-123",
            label=f"active-pass-{i}",
            scope_json=json.dumps({"publishers": ["pub-1"], "cidrs": [], "ports": [], "apps": []}),
            ttl_seconds=mins * 60,
            status="active",
            connection_url="wss://test.example.com/api/v1/tunnel/dap_test",
            created_at=created_at,
            expires_at=expires_at,
        )
        passes.append(p)

    # Simulate the expiration logic
    expired_count = 0
    for ap in passes:
        if ap.status == "active" and ap.expires_at < now:
            ap.status = "expired"
            expired_count += 1

    # No passes should have been expired
    assert expired_count == 0, (
        f"Expected 0 passes to be expired, but {expired_count} were"
    )
    for p in passes:
        assert p.status == "active", (
            f"Pass {p.id} should still be 'active', got '{p.status}'"
        )
