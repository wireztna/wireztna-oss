"""Groups — user membership and publisher access management."""

from fastapi import APIRouter, Depends, HTTPException, status
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy import select

from app.database import get_db
from app.models.group import Group, user_groups
from app.models.user import User
from app.models.publisher import Publisher
from app.models.access_policy import GroupPublisherPolicy, AccessRule
from app.schemas.schemas import (
    GroupCreate, GroupUpdate, GroupResponse, GroupMemberAdd, GroupPublisherAdd,
    AccessPolicyUpdate, AccessPolicyResponse, AccessRuleCreate, AccessRuleResponse,
)
from app.services.auth_service import require_admin, scoped_query, resolve_org_for_creation
from app.services.org_service import resolve_org_id

router = APIRouter()


# ─── CRUD ───

@router.get("", response_model=list[GroupResponse])
async def list_groups(
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(require_admin),
    org_id: str | None = None,
):
    query = select(Group).order_by(Group.name)
    query = scoped_query(query, Group, current_user, org_id)
    result = await db.execute(query)
    return result.scalars().all()


@router.post("", response_model=GroupResponse, status_code=status.HTTP_201_CREATED)
async def create_group(
    group: GroupCreate,
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(require_admin),
    org_id: str | None = None,
):
    # Resolve org_id: org_admin forced to their own org, super_admin uses explicit or default
    effective_org_id = resolve_org_for_creation(current_user, org_id)
    if not effective_org_id:
        effective_org_id = await resolve_org_id(None, db)

    # Check name uniqueness within the org (not globally)
    result = await db.execute(select(Group).where(Group.name == group.name, Group.org_id == effective_org_id))
    if result.scalar_one_or_none():
        raise HTTPException(status_code=409, detail="Group name already exists in this organization")

    new_group = Group(name=group.name, description=group.description, org_id=effective_org_id)
    db.add(new_group)
    await db.flush()
    await db.refresh(new_group)
    return new_group


@router.get("/{group_id}", response_model=GroupResponse)
async def get_group(group_id: str, db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    result = await db.execute(select(Group).where(Group.id == group_id))
    group = result.scalar_one_or_none()
    if not group:
        raise HTTPException(status_code=404, detail="Group not found")
    return group


@router.put("/{group_id}", response_model=GroupResponse)
async def update_group(group_id: str, update: GroupUpdate, db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    result = await db.execute(select(Group).where(Group.id == group_id))
    group = result.scalar_one_or_none()
    if not group:
        raise HTTPException(status_code=404, detail="Group not found")

    for field, value in update.model_dump(exclude_unset=True).items():
        setattr(group, field, value)

    await db.flush()
    await db.refresh(group)
    return group


@router.delete("/{group_id}", status_code=status.HTTP_204_NO_CONTENT)
async def delete_group(group_id: str, db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    result = await db.execute(select(Group).where(Group.id == group_id))
    group = result.scalar_one_or_none()
    if not group:
        raise HTTPException(status_code=404, detail="Group not found")
    await db.delete(group)


# ─── Members (users in this group) ───

@router.get("/{group_id}/members")
async def list_members(group_id: str, db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    result = await db.execute(select(Group).where(Group.id == group_id))
    group = result.scalar_one_or_none()
    if not group:
        raise HTTPException(status_code=404, detail="Group not found")

    await db.refresh(group, ["users"])
    return [{"id": u.id, "username": u.username, "email": u.email} for u in group.users]


@router.post("/{group_id}/members", status_code=status.HTTP_204_NO_CONTENT)
async def add_members(group_id: str, req: GroupMemberAdd, db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    result = await db.execute(select(Group).where(Group.id == group_id))
    group = result.scalar_one_or_none()
    if not group:
        raise HTTPException(status_code=404, detail="Group not found")

    await db.refresh(group, ["users"])

    for user_id in req.user_ids:
        result = await db.execute(select(User).where(User.id == user_id))
        user = result.scalar_one_or_none()
        if user and user not in group.users:
            group.users.append(user)


@router.delete("/{group_id}/members/{user_id}", status_code=status.HTTP_204_NO_CONTENT)
async def remove_member(group_id: str, user_id: str, db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    result = await db.execute(select(Group).where(Group.id == group_id))
    group = result.scalar_one_or_none()
    if not group:
        raise HTTPException(status_code=404, detail="Group not found")

    await db.refresh(group, ["users"])

    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if user and user in group.users:
        group.users.remove(user)


# ─── Publishers (which publishers this group can access) ───

@router.get("/{group_id}/publishers")
async def list_group_publishers(group_id: str, db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    """List publishers this group has access to."""
    result = await db.execute(select(Group).where(Group.id == group_id))
    group = result.scalar_one_or_none()
    if not group:
        raise HTTPException(status_code=404, detail="Group not found")

    await db.refresh(group, ["publishers"])
    return [
        {"id": p.id, "name": p.name, "location": p.location, "exposed_cidrs": p.exposed_cidrs}
        for p in group.publishers
    ]


@router.post("/{group_id}/publishers", status_code=status.HTTP_204_NO_CONTENT)
async def add_publishers(group_id: str, req: GroupPublisherAdd, db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    """Grant this group access to one or more publishers."""
    result = await db.execute(select(Group).where(Group.id == group_id))
    group = result.scalar_one_or_none()
    if not group:
        raise HTTPException(status_code=404, detail="Group not found")

    await db.refresh(group, ["publishers"])

    for publisher_id in req.publisher_ids:
        result = await db.execute(select(Publisher).where(Publisher.id == publisher_id))
        publisher = result.scalar_one_or_none()
        if publisher and publisher not in group.publishers:
            group.publishers.append(publisher)


@router.delete("/{group_id}/publishers/{publisher_id}", status_code=status.HTTP_204_NO_CONTENT)
async def remove_publisher_access(group_id: str, publisher_id: str, db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    """Revoke this group's access to a publisher."""
    result = await db.execute(select(Group).where(Group.id == group_id))
    group = result.scalar_one_or_none()
    if not group:
        raise HTTPException(status_code=404, detail="Group not found")

    await db.refresh(group, ["publishers"])

    result = await db.execute(select(Publisher).where(Publisher.id == publisher_id))
    publisher = result.scalar_one_or_none()
    if publisher and publisher in group.publishers:
        group.publishers.remove(publisher)


# ─── Access Policies (restrictive app rules per group↔publisher) ───

@router.get("/{group_id}/publishers/{publisher_id}/policy", response_model=AccessPolicyResponse)
async def get_access_policy(
    group_id: str, publisher_id: str,
    db: AsyncSession = Depends(get_db), _=Depends(require_admin),
):
    """Get the access policy for a group↔publisher pair.

    If no policy exists, returns a default 'unrestricted' response.
    """
    policy = await _get_or_default_policy(group_id, publisher_id, db)
    rules = []
    if policy and policy.id:
        rules_result = await db.execute(
            select(AccessRule).where(AccessRule.policy_id == policy.id).order_by(AccessRule.created_at)
        )
        rules = [AccessRuleResponse.model_validate(r) for r in rules_result.scalars().all()]

    return AccessPolicyResponse(
        id=policy.id if policy else "",
        group_id=group_id,
        publisher_id=publisher_id,
        access_policy=policy.access_policy if policy else "unrestricted",
        rules=rules,
        created_at=policy.created_at if policy else None,
    )


@router.put("/{group_id}/publishers/{publisher_id}/policy", response_model=AccessPolicyResponse)
async def set_access_policy(
    group_id: str, publisher_id: str,
    req: AccessPolicyUpdate,
    db: AsyncSession = Depends(get_db), _=Depends(require_admin),
):
    """Set the access policy for a group↔publisher pair.

    - "unrestricted": full CIDR access (default, same as current behavior)
    - "restricted": only allow specific apps defined as access rules
    """
    # Validate the group↔publisher relationship exists
    await _validate_group_publisher(group_id, publisher_id, db)

    # Get or create the policy
    result = await db.execute(
        select(GroupPublisherPolicy).where(
            GroupPublisherPolicy.group_id == group_id,
            GroupPublisherPolicy.publisher_id == publisher_id,
        )
    )
    policy = result.scalar_one_or_none()

    if policy:
        policy.access_policy = req.access_policy
    else:
        policy = GroupPublisherPolicy(
            group_id=group_id,
            publisher_id=publisher_id,
            access_policy=req.access_policy,
        )
        db.add(policy)

    await db.flush()
    await db.refresh(policy)

    # Load rules
    rules_result = await db.execute(
        select(AccessRule).where(AccessRule.policy_id == policy.id).order_by(AccessRule.created_at)
    )
    rules = [AccessRuleResponse.model_validate(r) for r in rules_result.scalars().all()]

    return AccessPolicyResponse(
        id=policy.id,
        group_id=group_id,
        publisher_id=publisher_id,
        access_policy=policy.access_policy,
        rules=rules,
        created_at=policy.created_at,
    )


@router.get("/{group_id}/publishers/{publisher_id}/rules", response_model=list[AccessRuleResponse])
async def list_access_rules(
    group_id: str, publisher_id: str,
    db: AsyncSession = Depends(get_db), _=Depends(require_admin),
):
    """List access rules for a restricted group↔publisher pair."""
    policy = await _get_policy(group_id, publisher_id, db)
    if not policy:
        return []

    result = await db.execute(
        select(AccessRule).where(AccessRule.policy_id == policy.id).order_by(AccessRule.created_at)
    )
    return result.scalars().all()


@router.post(
    "/{group_id}/publishers/{publisher_id}/rules",
    response_model=AccessRuleResponse,
    status_code=status.HTTP_201_CREATED,
)
async def add_access_rule(
    group_id: str, publisher_id: str,
    req: AccessRuleCreate,
    db: AsyncSession = Depends(get_db), _=Depends(require_admin),
):
    """Add an access rule to a group↔publisher pair.

    Automatically sets the policy to 'restricted' if not already set.
    """
    await _validate_group_publisher(group_id, publisher_id, db)

    # Get or create policy (auto-set to restricted when adding rules)
    result = await db.execute(
        select(GroupPublisherPolicy).where(
            GroupPublisherPolicy.group_id == group_id,
            GroupPublisherPolicy.publisher_id == publisher_id,
        )
    )
    policy = result.scalar_one_or_none()

    if not policy:
        policy = GroupPublisherPolicy(
            group_id=group_id,
            publisher_id=publisher_id,
            access_policy="restricted",
        )
        db.add(policy)
        await db.flush()
        await db.refresh(policy)
    elif policy.access_policy != "restricted":
        policy.access_policy = "restricted"
        await db.flush()

    # Create rule
    rule = AccessRule(
        policy_id=policy.id,
        target=req.target,
        port=req.port,
        protocol=req.protocol,
        name=req.name,
    )
    db.add(rule)
    await db.flush()
    await db.refresh(rule)
    return rule


@router.delete("/{group_id}/publishers/{publisher_id}/rules/{rule_id}", status_code=status.HTTP_204_NO_CONTENT)
async def delete_access_rule(
    group_id: str, publisher_id: str, rule_id: str,
    db: AsyncSession = Depends(get_db), _=Depends(require_admin),
):
    """Delete a specific access rule."""
    policy = await _get_policy(group_id, publisher_id, db)
    if not policy:
        raise HTTPException(status_code=404, detail="No policy found")

    result = await db.execute(
        select(AccessRule).where(AccessRule.id == rule_id, AccessRule.policy_id == policy.id)
    )
    rule = result.scalar_one_or_none()
    if not rule:
        raise HTTPException(status_code=404, detail="Rule not found")

    await db.delete(rule)


# ─── Helpers ───

async def _validate_group_publisher(group_id: str, publisher_id: str, db: AsyncSession):
    """Validate that the group exists and has the publisher assigned."""
    result = await db.execute(select(Group).where(Group.id == group_id))
    group = result.scalar_one_or_none()
    if not group:
        raise HTTPException(status_code=404, detail="Group not found")

    await db.refresh(group, ["publishers"])
    pub_ids = {p.id for p in group.publishers}
    if publisher_id not in pub_ids:
        raise HTTPException(status_code=404, detail="Publisher not assigned to this group")


async def _get_policy(group_id: str, publisher_id: str, db: AsyncSession) -> GroupPublisherPolicy | None:
    """Get existing policy or None."""
    result = await db.execute(
        select(GroupPublisherPolicy).where(
            GroupPublisherPolicy.group_id == group_id,
            GroupPublisherPolicy.publisher_id == publisher_id,
        )
    )
    return result.scalar_one_or_none()


async def _get_or_default_policy(group_id: str, publisher_id: str, db: AsyncSession) -> GroupPublisherPolicy | None:
    """Get existing policy or return a virtual 'unrestricted' default."""
    policy = await _get_policy(group_id, publisher_id, db)
    if policy:
        return policy
    # Return a virtual default (not persisted)
    from datetime import datetime
    return GroupPublisherPolicy(
        id="",
        group_id=group_id,
        publisher_id=publisher_id,
        access_policy="unrestricted",
        created_at=datetime.utcnow(),
    )
