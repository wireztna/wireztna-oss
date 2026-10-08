"""Organizations — multi-client logical isolation within a single broker."""

import re
import uuid
from datetime import datetime

from fastapi import APIRouter, Depends, HTTPException, status
from pydantic import BaseModel, field_validator
from sqlalchemy import select, func
from sqlalchemy.ext.asyncio import AsyncSession

from app.database import get_db
from app.models.organization import Organization
from app.models.user import User
from app.models.group import Group
from app.models.publisher import Publisher
from app.services.auth_service import require_admin

router = APIRouter()


# --- Schemas ---

class OrgCreate(BaseModel):
    name: str
    slug: str | None = None
    description: str | None = None

    @field_validator("slug", mode="before")
    @classmethod
    def auto_slug(cls, v, info):
        if v:
            return v
        # Auto-generate slug from name
        name = info.data.get("name", "")
        return re.sub(r"[^a-z0-9]+", "-", name.lower()).strip("-")


class OrgUpdate(BaseModel):
    name: str | None = None
    description: str | None = None


class OrgResponse(BaseModel):
    id: str
    name: str
    slug: str
    description: str | None
    created_at: datetime
    user_count: int = 0
    group_count: int = 0
    publisher_count: int = 0

    class Config:
        from_attributes = True


# --- Endpoints ---

@router.get("", response_model=list[OrgResponse])
async def list_organizations(
    db: AsyncSession = Depends(get_db),
    _=Depends(require_admin),
):
    """List all organizations with resource counts."""
    result = await db.execute(select(Organization).order_by(Organization.name))
    orgs = result.scalars().all()

    response = []
    for org in orgs:
        # Count resources per org
        users_count = (await db.execute(
            select(func.count()).where(User.org_id == org.id)
        )).scalar() or 0
        groups_count = (await db.execute(
            select(func.count()).where(Group.org_id == org.id)
        )).scalar() or 0
        publishers_count = (await db.execute(
            select(func.count()).where(Publisher.org_id == org.id)
        )).scalar() or 0

        response.append(OrgResponse(
            id=org.id,
            name=org.name,
            slug=org.slug,
            description=org.description,
            created_at=org.created_at,
            user_count=users_count,
            group_count=groups_count,
            publisher_count=publishers_count,
        ))

    return response


@router.post("", response_model=OrgResponse, status_code=status.HTTP_201_CREATED)
async def create_organization(
    data: OrgCreate,
    db: AsyncSession = Depends(get_db),
    _=Depends(require_admin),
):
    """Create a new organization."""
    # Validate slug uniqueness
    slug = data.slug or re.sub(r"[^a-z0-9]+", "-", data.name.lower()).strip("-")

    existing = await db.execute(select(Organization).where(
        (Organization.name == data.name) | (Organization.slug == slug)
    ))
    if existing.scalar_one_or_none():
        raise HTTPException(status_code=400, detail="Organization with this name or slug already exists")

    org = Organization(
        id=str(uuid.uuid4()),
        name=data.name,
        slug=slug,
        description=data.description,
        created_at=datetime.utcnow(),
    )
    db.add(org)
    await db.flush()

    return OrgResponse(
        id=org.id,
        name=org.name,
        slug=org.slug,
        description=org.description,
        created_at=org.created_at,
        user_count=0,
        group_count=0,
        publisher_count=0,
    )


@router.put("/{org_id}", response_model=OrgResponse)
async def update_organization(
    org_id: str,
    data: OrgUpdate,
    db: AsyncSession = Depends(get_db),
    _=Depends(require_admin),
):
    """Update an organization."""
    result = await db.execute(select(Organization).where(Organization.id == org_id))
    org = result.scalar_one_or_none()
    if not org:
        raise HTTPException(status_code=404, detail="Organization not found")

    if data.name is not None:
        org.name = data.name
    if data.description is not None:
        org.description = data.description

    users_count = (await db.execute(
        select(func.count()).where(User.org_id == org.id)
    )).scalar() or 0
    groups_count = (await db.execute(
        select(func.count()).where(Group.org_id == org.id)
    )).scalar() or 0
    publishers_count = (await db.execute(
        select(func.count()).where(Publisher.org_id == org.id)
    )).scalar() or 0

    return OrgResponse(
        id=org.id,
        name=org.name,
        slug=org.slug,
        description=org.description,
        created_at=org.created_at,
        user_count=users_count,
        group_count=groups_count,
        publisher_count=publishers_count,
    )


@router.delete("/{org_id}", status_code=status.HTTP_204_NO_CONTENT)
async def delete_organization(
    org_id: str,
    db: AsyncSession = Depends(get_db),
    _=Depends(require_admin),
):
    """Delete an organization. Fails if it still has resources assigned."""
    result = await db.execute(select(Organization).where(Organization.id == org_id))
    org = result.scalar_one_or_none()
    if not org:
        raise HTTPException(status_code=404, detail="Organization not found")

    # Prevent deletion if resources are still assigned
    users_count = (await db.execute(
        select(func.count()).where(User.org_id == org.id)
    )).scalar() or 0
    if users_count > 0:
        raise HTTPException(status_code=400, detail=f"Cannot delete: {users_count} users still in this organization")

    groups_count = (await db.execute(
        select(func.count()).where(Group.org_id == org.id)
    )).scalar() or 0
    if groups_count > 0:
        raise HTTPException(status_code=400, detail=f"Cannot delete: {groups_count} groups still in this organization")

    publishers_count = (await db.execute(
        select(func.count()).where(Publisher.org_id == org.id)
    )).scalar() or 0
    if publishers_count > 0:
        raise HTTPException(status_code=400, detail=f"Cannot delete: {publishers_count} publishers still in this organization")

    await db.delete(org)
