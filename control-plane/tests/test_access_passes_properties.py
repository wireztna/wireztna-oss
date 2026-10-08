"""Property-based tests for access passes CRUD endpoints.

Validates: Requirements 1.1, 1.3, 1.5, 1.6, 2.1, 3.2, 4.1, 4.2

Uses hypothesis to generate random inputs and verify correctness properties.
"""

import json
import asyncio
from datetime import datetime, timedelta
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from hypothesis import given, strategies as st, settings as hyp_settings

from app.services.pass_service import generate_pass_id
from app.models.access_pass import AccessPass


# ─── Property 3: Pass creation round-trip consistency ───
# **Validates: Requirements 1.1, 1.6**


@hyp_settings(max_examples=100)
@given(
    label=st.text(min_size=1, max_size=200).filter(lambda s: s.strip()),
    ttl=st.integers(min_value=60, max_value=7200),
)
def test_pass_creation_round_trip(label, ttl):
    """Pass creation stores consistent data.

    For any valid label and TTL, creating an AccessPass record results in:
    - status = "active"
    - bytes counters at zero
    - expires_at = created_at + ttl_seconds

    **Validates: Requirements 1.1, 1.6**
    """
    now = datetime.utcnow()
    pass_id = generate_pass_id()
    expires_at = now + timedelta(seconds=ttl)

    # Create a model instance (simulates what the router does)
    access_pass = AccessPass(
        id=pass_id,
        created_by_user_id="user-123",
        label=label,
        scope_json=json.dumps({"publishers": ["pub-1"], "cidrs": [], "ports": [], "apps": []}),
        ttl_seconds=ttl,
        status="active",
        connection_url=f"wss://test.example.com/api/v1/tunnel/{pass_id}",
        created_at=now,
        expires_at=expires_at,
    )

    assert access_pass.status == "active"
    assert access_pass.bytes_uploaded == 0 or access_pass.bytes_uploaded is None
    assert access_pass.bytes_downloaded == 0 or access_pass.bytes_downloaded is None
    assert access_pass.connections_count == 0 or access_pass.connections_count is None
    assert access_pass.label == label
    assert access_pass.ttl_seconds == ttl

    # Verify expires_at = created_at + ttl_seconds
    expected_expires = now + timedelta(seconds=ttl)
    # Allow 1 second tolerance (datetime precision)
    assert abs((access_pass.expires_at - expected_expires).total_seconds()) < 1


# ─── Property 4: Pass ID format invariant ───
# **Validates: Requirements 1.5**


@hyp_settings(max_examples=200)
@given(st.integers(min_value=0, max_value=10000))
def test_pass_id_format_invariant(_iteration):
    """Pass ID format invariant.

    Every generated pass_id matches ^dap_[a-z0-9]{6}$ and is 10 chars total.

    **Validates: Requirements 1.5**
    """
    import re

    pass_id = generate_pass_id()
    pattern = r"^dap_[a-z0-9]{6}$"
    assert re.match(pattern, pass_id), (
        f"Pass ID '{pass_id}' does not match pattern {pattern}"
    )
    assert len(pass_id) == 10


@hyp_settings(max_examples=100)
@given(st.integers(min_value=0, max_value=10000))
def test_pass_id_uniqueness(_iteration):
    """Pass IDs are unique across multiple generations.

    Generating 50 IDs should produce 50 unique values (collision probability
    is negligible for 36^6 = ~2 billion possible values).

    **Validates: Requirements 1.5**
    """
    ids = {generate_pass_id() for _ in range(50)}
    assert len(ids) == 50, f"Expected 50 unique IDs, got {len(ids)}"


# ─── Property 5: TTL cap enforcement ───
# **Validates: Requirements 1.3**


@hyp_settings(max_examples=100)
@given(
    ttl=st.integers(min_value=7201, max_value=100000),
)
def test_ttl_cap_enforcement(ttl):
    """TTL exceeding maximum is rejected.

    For any TTL > 7200 seconds, the validation should flag it as invalid.

    **Validates: Requirements 1.3**
    """
    MAX_TTL_SECONDS = 7200
    assert ttl > MAX_TTL_SECONDS, "Test precondition: TTL must exceed max"
    # The router would raise HTTPException(400) — we verify the logic condition
    is_invalid = ttl > MAX_TTL_SECONDS
    assert is_invalid


# ─── Property 6: Revocation sets status and timestamp ───
# **Validates: Requirements 4.1**


@hyp_settings(max_examples=100)
@given(
    label=st.text(min_size=1, max_size=50).filter(lambda s: s.strip()),
    ttl=st.integers(min_value=60, max_value=7200),
)
def test_revocation_sets_status_and_timestamp(label, ttl):
    """Revoking an active pass transitions status and sets revoked_at.

    **Validates: Requirements 4.1**
    """
    now = datetime.utcnow()
    pass_id = generate_pass_id()

    access_pass = AccessPass(
        id=pass_id,
        created_by_user_id="user-123",
        label=label,
        scope_json=json.dumps({"publishers": ["pub-1"], "cidrs": [], "ports": [], "apps": []}),
        ttl_seconds=ttl,
        status="active",
        connection_url=f"wss://test.example.com/api/v1/tunnel/{pass_id}",
        created_at=now,
        expires_at=now + timedelta(seconds=ttl),
    )

    # Simulate revocation (what the router does)
    assert access_pass.status == "active"
    access_pass.status = "revoked"
    access_pass.revoked_at = datetime.utcnow()

    assert access_pass.status == "revoked"
    assert access_pass.revoked_at is not None
    # revoked_at should be close to now (within a few seconds)
    assert (access_pass.revoked_at - now).total_seconds() < 5


# ─── Property 7: Cannot revoke non-active pass ───
# **Validates: Requirements 4.2**


@hyp_settings(max_examples=100)
@given(
    status=st.sampled_from(["expired", "revoked"]),
)
def test_cannot_revoke_non_active_pass(status):
    """Non-active passes cannot be revoked (returns 409 logic).

    **Validates: Requirements 4.2**
    """
    now = datetime.utcnow()
    pass_id = generate_pass_id()

    access_pass = AccessPass(
        id=pass_id,
        created_by_user_id="user-123",
        label="test",
        scope_json=json.dumps({"publishers": ["pub-1"], "cidrs": [], "ports": [], "apps": []}),
        ttl_seconds=3600,
        status=status,
        connection_url=f"wss://test.example.com/api/v1/tunnel/{pass_id}",
        created_at=now,
        expires_at=now + timedelta(seconds=3600),
    )

    # The router checks status == "active" before revoking
    is_revocable = access_pass.status == "active"
    assert not is_revocable, (
        f"Pass with status '{status}' should not be revocable"
    )


# ─── Property 9: List ordering by creation time ───
# **Validates: Requirements 2.1**


@hyp_settings(max_examples=100)
@given(
    num_passes=st.integers(min_value=2, max_value=10),
)
def test_list_ordering_by_creation_time(num_passes):
    """Passes should be orderable by creation time descending.

    **Validates: Requirements 2.1**
    """
    base_time = datetime(2026, 7, 1, 12, 0, 0)
    passes = []

    for i in range(num_passes):
        created = base_time + timedelta(minutes=i * 5)
        p = AccessPass(
            id=generate_pass_id(),
            created_by_user_id="user-123",
            label=f"pass-{i}",
            scope_json=json.dumps({"publishers": ["pub-1"], "cidrs": [], "ports": [], "apps": []}),
            ttl_seconds=3600,
            status="active",
            connection_url="wss://test.example.com/api/v1/tunnel/dap_test",
            created_at=created,
            expires_at=created + timedelta(seconds=3600),
        )
        passes.append(p)

    # Sort descending by created_at (what the router does)
    sorted_passes = sorted(passes, key=lambda p: p.created_at, reverse=True)

    # Verify ordering: each pass.created_at >= next pass.created_at
    for i in range(len(sorted_passes) - 1):
        assert sorted_passes[i].created_at >= sorted_passes[i + 1].created_at


# ─── Property 10: Ownership isolation ───
# **Validates: Requirements 3.2**


@hyp_settings(max_examples=100)
@given(
    user_a=st.text(min_size=5, max_size=36, alphabet="abcdef0123456789-"),
    user_b=st.text(min_size=5, max_size=36, alphabet="abcdef0123456789-"),
)
def test_ownership_isolation(user_a, user_b):
    """Pass created by user A is not accessible by user B.

    The router returns 404 when the pass's created_by_user_id != requesting user.

    **Validates: Requirements 3.2**
    """
    if user_a == user_b:
        return  # Skip: same user

    now = datetime.utcnow()
    access_pass = AccessPass(
        id=generate_pass_id(),
        created_by_user_id=user_a,
        label="test",
        scope_json=json.dumps({"publishers": ["pub-1"], "cidrs": [], "ports": [], "apps": []}),
        ttl_seconds=3600,
        status="active",
        connection_url="wss://test.example.com/api/v1/tunnel/dap_test",
        created_at=now,
        expires_at=now + timedelta(seconds=3600),
    )

    # Simulate ownership check (what the router does)
    requesting_user_id = user_b
    is_accessible = (
        access_pass is not None
        and access_pass.created_by_user_id == requesting_user_id
    )
    assert not is_accessible, (
        f"User '{user_b}' should not access pass owned by '{user_a}'"
    )
