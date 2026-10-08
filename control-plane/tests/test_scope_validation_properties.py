"""Property-based tests for scope validation service.

Validates: Requirements 1.2, 9.1, 9.2, 9.4, 10.3
"""

import pytest
import asyncio
from unittest.mock import MagicMock, AsyncMock, patch
from hypothesis import given, strategies as st, settings as hyp_settings
from fastapi import HTTPException

from app.services.scope_service import _cidr_is_subset, _find_best_publisher, validate_scope
from app.schemas.schemas import AccessPassScope


# ─── Mock Publisher class ───


class MockPublisher:
    """Lightweight mock matching the Publisher model interface."""

    def __init__(self, id, status, exposed_cidrs, published_apps=None, priority=100):
        self.id = id
        self.status = status
        self.exposed_cidrs = exposed_cidrs
        self.published_apps = published_apps or []
        self.priority = priority


# ─── Hypothesis strategies ───

# Strategy for generating publisher IDs (UUID-like strings)
publisher_id_st = st.text(
    alphabet="abcdef0123456789-",
    min_size=8,
    max_size=36,
).filter(lambda s: len(s) >= 8)

# Strategy for generating a /24 subnet in the 10.x.0.0 range
octet_st = st.integers(min_value=1, max_value=254)


# ─── Property 1: Scope validation rejects superset access ───
# **Validates: Requirements 1.2, 9.1, 9.4**


@hyp_settings(max_examples=100)
@given(
    accessible_ids=st.lists(publisher_id_st, min_size=1, max_size=5, unique=True),
    extra_ids=st.lists(publisher_id_st, min_size=1, max_size=3, unique=True),
)
@pytest.mark.asyncio
async def test_scope_rejects_superset_publishers(accessible_ids, extra_ids):
    """Scope validation rejects superset access.

    When the scope contains publisher IDs NOT in the user's accessible set,
    validate_scope must raise HTTPException(403).

    **Validates: Requirements 1.2**
    """
    # Ensure extra_ids are truly NOT in the accessible set
    extra_ids = [eid for eid in extra_ids if eid not in accessible_ids]
    if not extra_ids:
        return  # Skip: hypothesis generated overlapping sets

    # Build mock publishers for the accessible set
    accessible_publishers = [
        MockPublisher(
            id=pid,
            status="online",
            exposed_cidrs=["10.50.0.0/16"],
        )
        for pid in accessible_ids
    ]

    # Scope includes an ID that the user does NOT have access to
    scope = AccessPassScope(publishers=extra_ids[:1])

    # Mock the DB session to return our accessible publishers
    mock_db = AsyncMock()
    mock_result = MagicMock()
    mock_scalars = MagicMock()
    mock_scalars.unique.return_value.all.return_value = accessible_publishers
    mock_result.scalars.return_value = mock_scalars
    mock_db.execute = AsyncMock(return_value=mock_result)

    with pytest.raises(HTTPException) as exc_info:
        await validate_scope("user-1", scope, mock_db)

    assert exc_info.value.status_code == 403
    assert "not accessible" in exc_info.value.detail


# ─── Property 2: CIDR subset enforcement ───
# **Validates: Requirements 9.2**


@hyp_settings(max_examples=100)
@given(
    second_octet=octet_st,
    third_octet=st.integers(min_value=0, max_value=255),
)
def test_cidr_superset_rejected(second_octet, third_octet):
    """CIDR subset enforcement — superset is rejected.

    When the requested CIDR is broader than the exposed CIDR,
    _cidr_is_subset must return False.

    **Validates: Requirements 9.2**
    """
    # Exposed CIDR is a /24 (narrow)
    exposed = f"10.{second_octet}.{third_octet}.0/24"

    # Requested CIDR is a /16 (broader — superset of the /24)
    requested = f"10.{second_octet}.0.0/16"

    result = _cidr_is_subset(requested, [exposed])
    assert result is False, (
        f"Expected _cidr_is_subset('{requested}', ['{exposed}']) to be False "
        f"because /{16} is broader than /{24}"
    )


@hyp_settings(max_examples=100)
@given(
    second_octet=octet_st,
    third_octet=st.integers(min_value=0, max_value=255),
    host_octet=st.integers(min_value=0, max_value=255),
)
def test_cidr_subset_accepted(second_octet, third_octet, host_octet):
    """CIDR subset enforcement — genuine subset returns True.

    When the requested CIDR is a /32 (single host) within a /24,
    _cidr_is_subset must return True.

    **Validates: Requirements 9.2**
    """
    # Exposed CIDR is a /24
    exposed = f"10.{second_octet}.{third_octet}.0/24"

    # Requested CIDR is a /32 host within that /24 (always a subset)
    requested = f"10.{second_octet}.{third_octet}.{host_octet}/32"

    result = _cidr_is_subset(requested, [exposed])
    assert result is True, (
        f"Expected _cidr_is_subset('{requested}', ['{exposed}']) to be True "
        f"because a /32 within a /24 is a subset"
    )


# ─── Property 11: Publisher priority selection ───
# **Validates: Requirements 10.3**


@hyp_settings(max_examples=100)
@given(
    num_publishers=st.integers(min_value=2, max_value=5),
    priorities=st.lists(
        st.integers(min_value=1, max_value=1000),
        min_size=2,
        max_size=5,
    ),
    second_octet=octet_st,
)
def test_find_best_publisher_selects_lowest_priority(num_publishers, priorities, second_octet):
    """Publisher priority selection.

    _find_best_publisher always returns the publisher with the lowest
    priority number when multiple publishers expose the same CIDR.

    **Validates: Requirements 10.3**
    """
    # Trim priorities list to match num_publishers
    priorities = priorities[:num_publishers]
    if len(priorities) < num_publishers:
        # Pad if needed
        priorities = priorities + [100] * (num_publishers - len(priorities))

    # All publishers expose the same CIDR and are online
    target_cidr = f"10.{second_octet}.0.0/16"
    publishers = [
        MockPublisher(
            id=f"pub-{i}",
            status="online",
            exposed_cidrs=[target_cidr],
            priority=priorities[i],
        )
        for i in range(num_publishers)
    ]

    result = _find_best_publisher(publishers, target_cidr=target_cidr)

    assert result is not None, "Expected a publisher to be selected"
    expected_min_priority = min(priorities)
    assert result.priority == expected_min_priority, (
        f"Expected publisher with priority {expected_min_priority}, "
        f"got priority {result.priority}. Priorities were: {priorities}"
    )
