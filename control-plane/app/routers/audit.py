"""Audit log endpoints."""

from fastapi import APIRouter, Depends, Query
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy import select, desc

from app.database import get_db
from app.models.audit import AccessLog
from app.schemas.schemas import AccessLogEntry, AccessLogCreate
from app.services.auth_service import require_admin, require_internal

router = APIRouter()


@router.get("/logs", response_model=list[AccessLogEntry])
async def list_logs(
    user_id: str | None = None,
    action: str | None = None,
    limit: int = Query(default=100, le=1000),
    offset: int = 0,
    db: AsyncSession = Depends(get_db),
    _=Depends(require_admin),
):
    query = select(AccessLog).order_by(desc(AccessLog.timestamp))

    if user_id:
        query = query.where(AccessLog.user_id == user_id)
    if action:
        query = query.where(AccessLog.action == action)

    query = query.offset(offset).limit(limit)
    result = await db.execute(query)
    return result.scalars().all()


@router.post("/logs", status_code=201)
async def create_log(entry: AccessLogCreate, db: AsyncSession = Depends(get_db), _=Depends(require_internal)):
    """Ingest access log entries (called by broker agent). Requires internal auth."""
    log = AccessLog(
        user_id=entry.user_id,
        resource_id=entry.resource_id,
        publisher_id=entry.publisher_id,
        action=entry.action,
        detail=entry.detail,
        client_ip=entry.client_ip,
        duration_seconds=entry.duration_seconds,
        bytes_transferred=entry.bytes_transferred,
    )
    db.add(log)
    return {"status": "logged"}
