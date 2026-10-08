"""Scope validation service for delegated access passes.

Validates that a user's requested scope is a subset of their actual access,
and resolves the target (namespace, host, port) for the WebSocket relay.

Requirements: 9.1, 9.2, 9.3, 9.4, 10.3
"""

import json
import ipaddress
from pathlib import Path

from fastapi import HTTPException
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app.models.publisher import Publisher, group_publishers
from app.models.group import Group, user_groups
from app.schemas.schemas import AccessPassScope


NAMESPACE_MAP_PATH = Path("/etc/wireztna/namespace-map.json")


async def _get_user_accessible_publishers(user_id: str, db: AsyncSession) -> list[Publisher]:
    """Get all publishers accessible to a user via group memberships.

    Only returns publishers with status 'online'.
    """
    result = await db.execute(
        select(Publisher)
        .join(group_publishers, group_publishers.c.publisher_id == Publisher.id)
        .join(Group, Group.id == group_publishers.c.group_id)
        .join(user_groups, user_groups.c.group_id == Group.id)
        .where(
            user_groups.c.user_id == user_id,
            Publisher.status == "online",
        )
    )
    return list(result.scalars().unique().all())


def _cidr_is_subset(requested_cidr: str, exposed_cidrs: list[str]) -> bool:
    """Check if a requested CIDR is a subset of any CIDR in exposed_cidrs.

    Uses Python's ipaddress module for proper subnet containment checks.
    A CIDR is considered a subset if it is contained within (or equal to)
    any of the exposed CIDRs.
    """
    try:
        requested_net = ipaddress.ip_network(requested_cidr, strict=False)
    except ValueError:
        return False

    for exposed in exposed_cidrs:
        try:
            exposed_net = ipaddress.ip_network(exposed, strict=False)
        except ValueError:
            continue
        if requested_net.subnet_of(exposed_net):
            return True

    return False


def _port_matches_publisher(port: int, publisher: Publisher, requested_cidrs: list[str]) -> bool:
    """Check if a port is allowed for a publisher.

    A port is allowed if:
    1. It matches a published_app on the publisher, OR
    2. The full CIDR containing the target is accessible (no port restriction)

    When no specific CIDRs are requested but the publisher has exposed_cidrs,
    the full CIDR is considered accessible (any port allowed).
    """
    # Check if port matches a published_app
    for app in (publisher.published_apps or []):
        if isinstance(app, dict) and app.get("port") == port:
            return True

    # If no CIDRs are specified in the scope, but publisher has exposed_cidrs,
    # the full CIDR is accessible — any port is allowed
    if not requested_cidrs:
        return True

    # Check if the requested CIDRs are fully covered by exposed_cidrs
    # (meaning no port restriction applies — full network access)
    for cidr in requested_cidrs:
        if _cidr_is_subset(cidr, publisher.exposed_cidrs or []):
            return True

    return False


async def validate_scope(user_id: str, scope: AccessPassScope, db: AsyncSession) -> None:
    """Validate that a user's requested scope is within their accessible resources.

    Algorithm:
    1. Get user's accessible publishers via group memberships
    2. Verify each publisher in scope is in user's accessible set
    3. Verify each CIDR in scope is a subset of publishers' exposed_cidrs
    4. Verify each port matches published_apps or full CIDR is accessible

    Raises HTTPException(403) with descriptive message if validation fails.
    Returns None (silently) if the scope is valid.
    """
    accessible_publishers = await _get_user_accessible_publishers(user_id, db)
    accessible_publisher_ids = {p.id for p in accessible_publishers}
    accessible_publisher_map = {p.id: p for p in accessible_publishers}

    # Step 1: If no publishers specified in scope, infer from CIDRs/apps
    # For validation, we need at least publishers or CIDRs to work with
    scope_publisher_ids = set(scope.publishers) if scope.publishers else accessible_publisher_ids

    # Step 2: Verify each publisher in scope is accessible to the user
    for pub_id in scope.publishers:
        if pub_id not in accessible_publisher_ids:
            raise HTTPException(
                status_code=403,
                detail=f"Scope exceeds access: publisher {pub_id} not accessible",
            )

    # Step 3: Verify each CIDR is a subset of the accessible publishers' exposed_cidrs
    for cidr in scope.cidrs:
        cidr_covered = False
        for pub_id in scope_publisher_ids:
            pub = accessible_publisher_map.get(pub_id)
            if pub and _cidr_is_subset(cidr, pub.exposed_cidrs or []):
                cidr_covered = True
                break

        if not cidr_covered:
            raise HTTPException(
                status_code=403,
                detail=f"Scope exceeds access: CIDR {cidr} not covered by accessible publishers",
            )

    # Step 4: Verify each port matches a published_app or full CIDR is accessible
    for port in scope.ports:
        port_allowed = False
        for pub_id in scope_publisher_ids:
            pub = accessible_publisher_map.get(pub_id)
            if pub and _port_matches_publisher(port, pub, scope.cidrs):
                port_allowed = True
                break

        if not port_allowed:
            raise HTTPException(
                status_code=403,
                detail=f"Scope exceeds access: port {port} not allowed on any accessible publisher",
            )


def _load_namespace_map() -> dict:
    """Load the namespace map from /etc/wireztna/namespace-map.json.

    Returns a dict mapping publisher_id -> namespace_name.
    Falls back to deriving namespace from publisher_id prefix if file not found.
    """
    try:
        content = NAMESPACE_MAP_PATH.read_text()
        return json.loads(content)
    except (FileNotFoundError, json.JSONDecodeError):
        return {}


def _find_best_publisher(
    publishers: list[Publisher],
    target_cidr: str | None = None,
    target_port: int | None = None,
) -> Publisher | None:
    """Find the best publisher for a target based on priority.

    When multiple publishers expose the same CIDR, selects the one with
    the lowest priority number (highest priority) that is online.
    """
    candidates = []

    for pub in publishers:
        if pub.status != "online":
            continue

        # If a specific CIDR is requested, check if this publisher exposes it
        if target_cidr:
            if not _cidr_is_subset(target_cidr, pub.exposed_cidrs or []):
                continue

        # If a specific port is requested, check if the publisher serves it
        if target_port:
            has_port = any(
                isinstance(app, dict) and app.get("port") == target_port
                for app in (pub.published_apps or [])
            )
            # Port is allowed if either it matches an app or full CIDR access
            has_full_cidr = target_cidr and _cidr_is_subset(
                target_cidr, pub.exposed_cidrs or []
            )
            if not has_port and not has_full_cidr:
                continue

        candidates.append(pub)

    if not candidates:
        return None

    # Sort by priority (lower number = higher priority)
    candidates.sort(key=lambda p: p.priority)
    return candidates[0]


async def resolve_target(
    scope: AccessPassScope, db: AsyncSession
) -> tuple[str, str, int]:
    """Resolve scope to a target: (namespace_name, host, port).

    Determines the appropriate publisher namespace, target host IP, and port
    based on the scope definition.

    Returns:
        Tuple of (namespace_name, host, port) for the TCP connection.

    Raises:
        HTTPException(403) if scope cannot be resolved to a valid target.
    """
    # Load namespace map
    ns_map = _load_namespace_map()

    # Determine target host and port from scope
    target_host: str | None = None
    target_port: int | None = None

    # Priority 1: If apps are specified, resolve from published_apps
    # Priority 2: If CIDRs and ports are specified, use those directly
    # Priority 3: If only CIDRs are specified, use the first CIDR host

    # Get accessible publishers in scope
    if scope.publishers:
        result = await db.execute(
            select(Publisher).where(
                Publisher.id.in_(scope.publishers),
                Publisher.status == "online",
            )
        )
        publishers = list(result.scalars().all())
    else:
        # No publishers specified — this shouldn't happen after validation
        raise HTTPException(
            status_code=403,
            detail="Cannot resolve target: no publishers in scope",
        )

    if not publishers:
        raise HTTPException(
            status_code=403,
            detail="Cannot resolve target: no online publishers found",
        )

    # Resolve target from apps (highest priority)
    if scope.apps:
        for pub in publishers:
            for app in (pub.published_apps or []):
                if isinstance(app, dict) and app.get("name") in scope.apps:
                    target_host = app.get("target")
                    target_port = app.get("port")
                    # Use this publisher
                    namespace = _resolve_namespace(pub, ns_map)
                    if namespace and target_host and target_port:
                        return (namespace, target_host, target_port)

    # Resolve target from CIDRs + ports
    if scope.cidrs and scope.ports:
        target_cidr = scope.cidrs[0]
        target_port = scope.ports[0]

        # Extract host from CIDR (for /32 it's the host itself)
        try:
            net = ipaddress.ip_network(target_cidr, strict=False)
            if net.prefixlen == 32:
                target_host = str(net.network_address)
            else:
                # For broader CIDRs, use the network address
                # (agent will specify actual target via the TCP connection)
                target_host = str(net.network_address)
        except ValueError:
            raise HTTPException(
                status_code=403,
                detail=f"Cannot resolve target: invalid CIDR {target_cidr}",
            )

        # Find best publisher for this CIDR (priority-based)
        best_pub = _find_best_publisher(publishers, target_cidr, target_port)
        if best_pub:
            namespace = _resolve_namespace(best_pub, ns_map)
            if namespace:
                return (namespace, target_host, target_port)

    # Resolve from CIDRs only (no port specified — use first published_app port)
    if scope.cidrs:
        target_cidr = scope.cidrs[0]
        try:
            net = ipaddress.ip_network(target_cidr, strict=False)
            target_host = str(net.network_address) if net.prefixlen != 32 else str(net.network_address)
        except ValueError:
            raise HTTPException(
                status_code=403,
                detail=f"Cannot resolve target: invalid CIDR {target_cidr}",
            )

        best_pub = _find_best_publisher(publishers, target_cidr)
        if best_pub:
            namespace = _resolve_namespace(best_pub, ns_map)
            # Try to find a port from published_apps matching the CIDR
            port = _find_port_for_cidr(best_pub, target_cidr)
            if namespace and port:
                return (namespace, target_host, port)
            elif namespace and scope.ports:
                return (namespace, target_host, scope.ports[0])

    raise HTTPException(
        status_code=403,
        detail="Cannot resolve target: unable to determine namespace, host, or port from scope",
    )


def _resolve_namespace(publisher: Publisher, ns_map: dict) -> str | None:
    """Resolve a publisher to its namespace name.

    Uses the namespace map first, falls back to convention: ns-{publisher_id[:8]}.
    """
    # Try namespace map (publisher_id -> namespace_name)
    ns_name = ns_map.get(publisher.id)
    if ns_name:
        return ns_name

    # Fallback: derive from publisher ID (first 8 chars)
    return f"ns-{publisher.id[:8]}"


def _find_port_for_cidr(publisher: Publisher, target_cidr: str) -> int | None:
    """Find the first published_app port that matches a target CIDR.

    Checks if any published_app's target IP falls within the target CIDR.
    """
    try:
        net = ipaddress.ip_network(target_cidr, strict=False)
    except ValueError:
        return None

    for app in (publisher.published_apps or []):
        if not isinstance(app, dict):
            continue
        app_target = app.get("target")
        app_port = app.get("port")
        if not app_target or not app_port:
            continue

        try:
            app_ip = ipaddress.ip_address(app_target)
            if app_ip in net:
                return app_port
        except ValueError:
            # app_target might be a hostname, skip
            continue

    return None
