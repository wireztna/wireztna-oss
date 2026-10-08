"""System announcements endpoints."""

from fastapi import APIRouter, Depends, HTTPException, status
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy import select
from pydantic import BaseModel, Field
from datetime import datetime

from app.database import get_db
from app.models.announcement import SystemAnnouncement
from app.services.auth_service import get_current_user, require_super_admin

router = APIRouter()


# ─── Schemas ───

class AnnouncementCreate(BaseModel):
    title: str = Field(..., min_length=1, max_length=200)
    message: str = Field(..., min_length=1)
    type: str = Field(default="info", pattern="^(info|warning|success|update)$")


class AnnouncementUpdate(BaseModel):
    title: str | None = None
    message: str | None = None
    type: str | None = Field(default=None, pattern="^(info|warning|success|update)$")
    active: bool | None = None


class AnnouncementResponse(BaseModel):
    id: str
    title: str
    message: str
    type: str
    active: bool
    created_by: str | None
    created_at: datetime

    class Config:
        from_attributes = True


# ─── Public endpoint (authenticated users) ───

@router.get("/active", response_model=list[AnnouncementResponse])
async def get_active_announcements(
    db: AsyncSession = Depends(get_db),
    _=Depends(get_current_user),
):
    """Get all active announcements (shown to all authenticated users)."""
    result = await db.execute(
        select(SystemAnnouncement)
        .where(SystemAnnouncement.active == True)
        .order_by(SystemAnnouncement.created_at.desc())
    )
    return result.scalars().all()


# ─── Admin endpoints ───

@router.get("/", response_model=list[AnnouncementResponse])
async def list_announcements(
    db: AsyncSession = Depends(get_db),
    _=Depends(require_super_admin),
):
    """List all announcements (active and inactive)."""
    result = await db.execute(
        select(SystemAnnouncement).order_by(SystemAnnouncement.created_at.desc())
    )
    return result.scalars().all()


@router.post("/", response_model=AnnouncementResponse, status_code=status.HTTP_201_CREATED)
async def create_announcement(
    data: AnnouncementCreate,
    db: AsyncSession = Depends(get_db),
    current_user=Depends(require_super_admin),
):
    """Create a new system announcement."""
    announcement = SystemAnnouncement(
        title=data.title,
        message=data.message,
        type=data.type,
        created_by=current_user.get("sub"),
    )
    db.add(announcement)
    await db.flush()
    await db.refresh(announcement)
    return announcement


@router.put("/{announcement_id}", response_model=AnnouncementResponse)
async def update_announcement(
    announcement_id: str,
    data: AnnouncementUpdate,
    db: AsyncSession = Depends(get_db),
    _=Depends(require_super_admin),
):
    """Update an announcement."""
    result = await db.execute(
        select(SystemAnnouncement).where(SystemAnnouncement.id == announcement_id)
    )
    announcement = result.scalar_one_or_none()
    if not announcement:
        raise HTTPException(status_code=404, detail="Announcement not found")

    for field, value in data.model_dump(exclude_unset=True).items():
        setattr(announcement, field, value)

    await db.flush()
    await db.refresh(announcement)
    return announcement


@router.delete("/{announcement_id}", status_code=status.HTTP_204_NO_CONTENT)
async def delete_announcement(
    announcement_id: str,
    db: AsyncSession = Depends(get_db),
    _=Depends(require_super_admin),
):
    """Delete an announcement permanently."""
    result = await db.execute(
        select(SystemAnnouncement).where(SystemAnnouncement.id == announcement_id)
    )
    announcement = result.scalar_one_or_none()
    if not announcement:
        raise HTTPException(status_code=404, detail="Announcement not found")
    await db.delete(announcement)
