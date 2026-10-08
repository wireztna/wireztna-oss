"""Organization filtering dependency for API endpoints."""

from typing import Optional

from fastapi import Query
from sqlalchemy import text
from sqlalchemy.ext.asyncio import AsyncSession


def get_org_filter(org_id: Optional[str] = Query(None, description="Filter by organization ID")) -> Optional[str]:
    """FastAPI dependency that extracts org_id from query params.

    Used by admin endpoints to filter resources by organization.
    - If org_id is provided: filter to that org only
    - If org_id is None: return all (admin sees everything)
    """
    return org_id


async def resolve_org_id(org_id: Optional[str], db: AsyncSession) -> Optional[str]:
    """Resolve org_id for resource creation — ensures no NULL org_id in DB.

    If org_id is provided (non-empty string), returns it as-is.
    If org_id is None or empty, returns the default (first) organization's ID.
    This prevents resources being created with NULL org_id and becoming
    invisible when the UI filters by organization.
    """
    if org_id and org_id.strip():
        return org_id
    # Fall back to the first org (the auto-created 'Default' org)
    result = await db.execute(text("SELECT id FROM organizations ORDER BY created_at ASC LIMIT 1"))
    row = result.first()
    return row[0] if row else None
