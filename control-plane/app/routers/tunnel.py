"""WebSocket tunnel relay endpoint for delegated access passes.

Bridges agent WebSocket traffic to internal TCP resources via publisher
network namespaces. One WebSocket connection = one TCP connection to the
target resource specified in the pass scope.

Requirements: 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 5.7, 5.8, 7.4, 7.5
"""

import asyncio
import json
import logging
from datetime import datetime

from fastapi import APIRouter, WebSocket, WebSocketDisconnect
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app.database import async_session
from app.models.access_pass import AccessPass
from app.models.audit import AccessLog
from app.schemas.schemas import AccessPassScope
from app.services.scope_service import resolve_target
from app.services.tunnel_service import (
    open_tcp_in_namespace,
    TargetUnreachableError,
    NamespaceNotFoundError,
    TunnelError,
)

# Import the shared active tunnels dict from the CRUD router
from app.routers.access_passes import _active_tunnels

logger = logging.getLogger(__name__)

router = APIRouter()

# Chunk size for TCP reads
TCP_READ_SIZE = 65536


@router.get("/api/v1/tunnel/{pass_id}/ttl")
async def get_pass_ttl(pass_id: str):
    """Lightweight endpoint returning remaining TTL for a pass. No auth required.
    Used by wireztna-proxy to display accurate countdown."""
    async with async_session() as db:
        result = await db.execute(
            select(AccessPass.expires_at, AccessPass.status).where(AccessPass.id == pass_id)
        )
        row = result.first()
        if not row or row[1] != "active":
            return {"ttl_seconds": 0}
        remaining = max(0, int((row[0] - datetime.utcnow()).total_seconds()))
        return {"ttl_seconds": remaining}


@router.websocket("/api/v1/tunnel/{pass_id}")
async def tunnel_endpoint(websocket: WebSocket, pass_id: str):
    """WebSocket tunnel relay for delegated access passes.

    Flow:
    1. Validate pass (exists, active, not expired, not revoked)
    2. Check connections_count < max_connections
    3. Resolve target from scope → (namespace, host, port)
    4. Open TCP in namespace
    5. Accept WebSocket
    6. Start bidirectional relay (WS↔TCP)
    7. Track bytes, handle expiration/revocation mid-tunnel
    """
    # ─── 1. Validate the pass ───
    async with async_session() as db:
        result = await db.execute(
            select(AccessPass).where(AccessPass.id == pass_id)
        )
        access_pass = result.scalar_one_or_none()

        if not access_pass:
            # Accept WS, send error, close — so client gets the reason
            await websocket.accept()
            await websocket.send_json({"error": "pass_not_found", "message": "Pass not found"})
            await websocket.close(code=4000, reason="Pass not found")
            return

        if access_pass.status != "active":
            await websocket.accept()
            await websocket.send_json({"error": "pass_not_active", "message": f"Pass not active (status: {access_pass.status})"})
            await websocket.close(code=4000, reason=f"Pass not active (status: {access_pass.status})")
            return

        now = datetime.utcnow()
        if now >= access_pass.expires_at:
            # Mark as expired
            access_pass.status = "expired"
            await db.commit()
            await websocket.accept()
            await websocket.send_json({"error": "pass_expired", "message": "Pass expired"})
            await websocket.close(code=4001, reason="Pass expired")
            return

        # ─── 2. Check max concurrent connections ───
        # Clean stale entries (closed WebSockets that weren't properly removed)
        if pass_id in _active_tunnels:
            _active_tunnels[pass_id] = [
                ws for ws in _active_tunnels[pass_id]
                if ws.client_state.name != "DISCONNECTED"
            ]
            if not _active_tunnels[pass_id]:
                del _active_tunnels[pass_id]

        active_count = len(_active_tunnels.get(pass_id, []))
        if active_count >= access_pass.max_connections:
            await websocket.accept()
            await websocket.send_json({"error": "max_connections", "message": "Maximum connections reached"})
            await websocket.close(code=4029, reason="Maximum connections reached")
            return

        # ─── 3. Resolve target ───
        scope = AccessPassScope(**json.loads(access_pass.scope_json))

        try:
            namespace, host, port = await resolve_target(scope, db)
        except Exception as e:
            logger.error(f"Cannot resolve target for pass {pass_id}: {e}")
            await websocket.accept()
            await websocket.send_json({"error": "target_unreachable", "message": f"Cannot resolve target: publisher may be offline"})
            await websocket.close(code=4003, reason="Target unreachable")
            return

    # ─── 4. Open TCP in namespace ───
    try:
        proc = await open_tcp_in_namespace(namespace, host, port)
    except TargetUnreachableError as e:
        logger.warning(f"Target unreachable for pass {pass_id}: {e}")
        await websocket.accept()
        await websocket.send_json({"error": "target_unreachable", "message": f"Target {host}:{port} is not reachable from the publisher"})
        await websocket.close(code=4003, reason="Target unreachable")
        return
    except NamespaceNotFoundError as e:
        logger.error(f"Namespace not found for pass {pass_id}: {e}")
        await websocket.accept()
        await websocket.send_json({"error": "target_unreachable", "message": "Publisher namespace not found — publisher may be offline"})
        await websocket.close(code=4003, reason="Target unreachable")
        return
    except TunnelError as e:
        logger.error(f"Tunnel error for pass {pass_id}: {e}")
        await websocket.accept()
        await websocket.send_json({"error": "tunnel_error", "message": f"Tunnel setup failed: {e}"})
        await websocket.close(code=4003, reason="Target unreachable")
        return

    # ─── 5. Accept WebSocket ───
    await websocket.accept()

    # Send "ready" signal — tells the client that TCP is connected and relay can start
    await websocket.send_json({"status": "ready", "target": f"{host}:{port}", "namespace": namespace})

    # Register in active tunnels
    if pass_id not in _active_tunnels:
        _active_tunnels[pass_id] = []
    _active_tunnels[pass_id].append(websocket)

    # Increment connections count
    async with async_session() as db:
        result = await db.execute(
            select(AccessPass).where(AccessPass.id == pass_id)
        )
        ap = result.scalar_one_or_none()
        if ap:
            ap.connections_count = (ap.connections_count or 0) + 1
            ap.last_activity_at = datetime.utcnow()
            await db.commit()

    # Write audit log
    async with async_session() as db:
        db.add(AccessLog(
            user_id=access_pass.created_by_user_id,
            resource_id=pass_id,
            action="tunnel_connected",
            detail=f"Agent connected to {host}:{port} via {namespace}",
        ))
        await db.commit()

    # ─── 6. Bidirectional relay ───
    bytes_uploaded = 0
    bytes_downloaded = 0
    close_reason = "normal"

    # Shared byte counters (mutable list so _relay can update them)
    byte_counters = [0, 0]  # [uploaded, downloaded]

    # Compute TTL remaining for expiration timer
    ttl_remaining = (access_pass.expires_at - datetime.utcnow()).total_seconds()

    try:
        await _relay(
            websocket=websocket,
            proc=proc,
            pass_id=pass_id,
            ttl_remaining=max(0, ttl_remaining),
            max_bytes=access_pass.max_bytes,
            created_by=access_pass.created_by_user_id,
            byte_counters=byte_counters,
        )
    except _TunnelExpired:
        close_reason = "expired"
    except _TunnelRevoked:
        close_reason = "revoked"
    except _TrafficLimitExceeded:
        close_reason = "traffic_limit"
    except WebSocketDisconnect:
        close_reason = "client_disconnected"
    except Exception as e:
        logger.error(f"Relay error for pass {pass_id}: {e}")
        close_reason = "error"
    finally:
        # Kill subprocess if still running
        if proc.returncode is None:
            try:
                proc.kill()
                await proc.wait()
            except Exception:
                pass

        # Remove from active tunnels
        if pass_id in _active_tunnels:
            try:
                _active_tunnels[pass_id].remove(websocket)
            except ValueError:
                pass
            if not _active_tunnels[pass_id]:
                del _active_tunnels[pass_id]

        # Update pass byte counters in DB (always, even on error)
        try:
            async with async_session() as db:
                result = await db.execute(
                    select(AccessPass).where(AccessPass.id == pass_id)
                )
                ap = result.scalar_one_or_none()
                if ap:
                    ap.bytes_uploaded = (ap.bytes_uploaded or 0) + byte_counters[0]
                    ap.bytes_downloaded = (ap.bytes_downloaded or 0) + byte_counters[1]
                    ap.last_activity_at = datetime.utcnow()
                    await db.commit()
                    logger.info(f"Pass {pass_id}: persisted {byte_counters[0]}↑ {byte_counters[1]}↓ bytes")

                # Write disconnect audit log
                db.add(AccessLog(
                    user_id=access_pass.created_by_user_id,
                    resource_id=pass_id,
                    action="tunnel_disconnected",
                    detail=f"Disconnected ({close_reason}) — ↑{byte_counters[0]} ↓{byte_counters[1]} bytes",
                ))
                await db.commit()
        except Exception as e:
            logger.error(f"Failed to persist bytes for pass {pass_id}: {e}")


class _TunnelExpired(Exception):
    pass


class _TunnelRevoked(Exception):
    pass


class _TrafficLimitExceeded(Exception):
    pass


async def _relay(
    websocket: WebSocket,
    proc: asyncio.subprocess.Process,
    pass_id: str,
    ttl_remaining: float,
    max_bytes: int | None,
    created_by: str,
    byte_counters: list,
):
    """Bidirectional relay between WebSocket and subprocess stdin/stdout.

    Two concurrent tasks:
    - ws_to_tcp: reads WS binary frames → writes to proc.stdin
    - tcp_to_ws: reads proc.stdout → sends as WS binary frames

    Also runs an expiration timer that fires when the pass TTL expires.
    byte_counters is a mutable [uploaded, downloaded] list updated in-place.
    """
    done = asyncio.Event()

    async def ws_to_tcp():
        try:
            while not done.is_set():
                data = await websocket.receive_bytes()
                if not data:
                    break
                proc.stdin.write(data)
                await proc.stdin.drain()
                byte_counters[0] += len(data)

                # Check traffic limit
                if max_bytes and (byte_counters[0] + byte_counters[1]) > max_bytes:
                    raise _TrafficLimitExceeded()
        except WebSocketDisconnect:
            pass
        except _TrafficLimitExceeded:
            raise
        except Exception:
            pass
        finally:
            done.set()

    async def tcp_to_ws():
        try:
            while not done.is_set():
                data = await proc.stdout.read(TCP_READ_SIZE)
                if not data:
                    break
                await websocket.send_bytes(data)
                byte_counters[1] += len(data)

                # Check traffic limit
                if max_bytes and (byte_counters[0] + byte_counters[1]) > max_bytes:
                    raise _TrafficLimitExceeded()
        except _TrafficLimitExceeded:
            raise
        except Exception:
            pass
        finally:
            done.set()

    async def expiration_timer():
        """Closes the tunnel when the pass TTL expires."""
        await asyncio.sleep(ttl_remaining)
        # Mark pass as expired
        async with async_session() as db:
            result = await db.execute(
                select(AccessPass).where(AccessPass.id == pass_id)
            )
            ap = result.scalar_one_or_none()
            if ap and ap.status == "active":
                ap.status = "expired"
                await db.commit()
        raise _TunnelExpired()

    async def revocation_checker():
        """Periodically checks if the pass has been revoked."""
        while not done.is_set():
            await asyncio.sleep(5)
            async with async_session() as db:
                result = await db.execute(
                    select(AccessPass.status).where(AccessPass.id == pass_id)
                )
                status = result.scalar_one_or_none()
                if status and status != "active":
                    raise _TunnelRevoked()

    # Create tasks
    tasks = [
        asyncio.create_task(ws_to_tcp()),
        asyncio.create_task(tcp_to_ws()),
        asyncio.create_task(expiration_timer()),
        asyncio.create_task(revocation_checker()),
    ]

    try:
        # Wait for any task to complete (or raise)
        _done, pending = await asyncio.wait(
            tasks, return_when=asyncio.FIRST_EXCEPTION
        )

        # Check if any completed task raised an exception
        for task in _done:
            if task.exception():
                raise task.exception()

    finally:
        done.set()
        # Cancel all pending tasks
        for task in tasks:
            if not task.done():
                task.cancel()
                try:
                    await task
                except (asyncio.CancelledError, Exception):
                    pass

        # Persist byte counters to DB (must be here — endpoint's finally may not run for WS)
        try:
            async with async_session() as db:
                result = await db.execute(
                    select(AccessPass).where(AccessPass.id == pass_id)
                )
                ap = result.scalar_one_or_none()
                if ap:
                    ap.bytes_uploaded = (ap.bytes_uploaded or 0) + byte_counters[0]
                    ap.bytes_downloaded = (ap.bytes_downloaded or 0) + byte_counters[1]
                    ap.last_activity_at = datetime.utcnow()
                    await db.commit()
                    logger.info(f"Pass {pass_id}: persisted {byte_counters[0]}↑ {byte_counters[1]}↓ bytes")
        except Exception as e:
            logger.error(f"Failed to persist bytes for {pass_id}: {e}")

        # Close WebSocket with appropriate code
        try:
            if isinstance(tasks[2].exception() if tasks[2].done() else None, _TunnelExpired):
                await websocket.close(code=4001, reason="Pass expired")
            elif isinstance(tasks[3].exception() if tasks[3].done() else None, _TunnelRevoked):
                await websocket.close(code=4002, reason="Pass revoked")
            else:
                await websocket.close(code=1000, reason="Normal close")
        except Exception:
            pass
