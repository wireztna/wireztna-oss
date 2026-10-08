import json
from datetime import datetime, timedelta

import pytest
import pytest_asyncio
from fastapi import HTTPException, Request
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker, create_async_engine

import app.models  # noqa: F401 -- register all metadata and relationship targets
from app.database import Base
from app.models.audit import AccessLog
from app.models.group import Group
from app.models.publisher import Publisher
from app.models.session import ClientSession
from app.models.user import User
from app.routers.sessions import (
    _broker_wireguard_endpoint,
    get_available_groups,
    renew_session,
)


@pytest_asyncio.fixture
async def db_session():
    engine = create_async_engine("sqlite+aiosqlite:///:memory:", echo=False)
    async with engine.begin() as conn:
        await conn.run_sync(Base.metadata.create_all)

    session_factory = async_sessionmaker(engine, class_=AsyncSession, expire_on_commit=False)
    async with session_factory() as session:
        yield session

    await engine.dispose()


def test_broker_wireguard_endpoint_includes_configured_port(monkeypatch):
    monkeypatch.setattr("app.routers.sessions.settings.broker_public_endpoint", "wg.example.test")
    monkeypatch.setattr("app.routers.sessions.settings.broker_wg_port", 51999)

    assert _broker_wireguard_endpoint() == "wg.example.test:51999"


def test_broker_wireguard_endpoint_fallback_includes_configured_port(monkeypatch):
    monkeypatch.setattr("app.routers.sessions.settings.broker_public_endpoint", "")
    monkeypatch.setattr("app.routers.sessions.settings.broker_overlay_ip", "10.200.0.1")
    monkeypatch.setattr("app.routers.sessions.settings.broker_wg_port", 51999)

    assert _broker_wireguard_endpoint() == "10.200.0.1:51999"


@pytest.mark.asyncio
async def test_available_groups_deduplicates_and_orders_shared_exit_nodes(db_session):
    user = User(
        id="user-catalog",
        username="Catalog User",
        email="catalog@example.test",
        vpn_mode=True,
    )
    group_a = Group(id="group-a", name="Group A")
    group_b = Group(id="group-b", name="Group B")
    first = Publisher(
        id="exit-first",
        name="Zulu",
        status="online",
        exit_node=True,
        publisher_index=1,
    )
    shared = Publisher(
        id="exit-shared",
        name="Shared",
        status="online",
        exit_node=True,
        publisher_index=2,
    )
    unindexed = Publisher(
        id="exit-unindexed",
        name="Alpha",
        status="online",
        exit_node=True,
        publisher_index=None,
    )
    user.groups.extend([group_a, group_b])
    group_a.publishers.extend([shared, unindexed])
    group_b.publishers.extend([first, shared])
    db_session.add(user)
    await db_session.flush()

    catalog = await get_available_groups(db=db_session, current_user={"sub": user.id})

    assert [node["id"] for node in catalog["exit_nodes"]] == [
        "exit-first",
        "exit-shared",
        "exit-unindexed",
    ]
    resources_by_group = {
        group["id"]: {publisher["id"] for publisher in group["publishers"]}
        for group in catalog["groups"]
    }
    assert "exit-shared" in resources_by_group["group-a"]
    assert "exit-shared" in resources_by_group["group-b"]


@pytest.mark.asyncio
async def test_reused_session_selection_audit_is_atomic_and_secret_free(db_session):
    sentinel_psk = "SENTINEL-PSK-MUST-NOT-APPEAR-IN-AUDIT"
    user = User(
        id="user-renew",
        username="Renew User",
        email="renew@example.test",
        overlay_ip="10.200.1.99",
        vpn_mode=True,
    )
    old_group = Group(id="group-old", name="Old Group")
    new_group = Group(id="group-new", name="New Group")
    shared_exit = Publisher(
        id="exit-shared",
        name="Shared Exit",
        status="online",
        exit_node=True,
        publisher_index=1,
    )
    inaccessible_exit = Publisher(
        id="exit-private",
        name="Private Exit",
        status="online",
        exit_node=True,
        publisher_index=2,
    )
    user.groups.extend([old_group, new_group])
    old_group.publishers.extend([shared_exit, inaccessible_exit])
    new_group.publishers.append(shared_exit)
    session = ClientSession(
        id="session-existing",
        user_id=user.id,
        preshared_key=sentinel_psk,
        expires_at=datetime.utcnow() + timedelta(hours=1),
        selected_group_id=old_group.id,
        exit_node_publisher_id=None,
        is_active=True,
    )
    db_session.add_all([user, session])
    await db_session.flush()
    request = Request({"type": "http", "client": ("203.0.113.8", 4242)})

    renewed = await renew_session(
        request=request,
        body=None,
        group_id=new_group.id,
        exit_node_id=shared_exit.id,
        db=db_session,
        current_user={"sub": user.id},
    )

    assert renewed.session_id == session.id
    assert renewed.preshared_key == sentinel_psk
    audit_rows = (await db_session.execute(
        select(AccessLog).where(AccessLog.action == "session_selection_changed")
    )).scalars().all()
    assert len(audit_rows) == 1
    audit = audit_rows[0]
    assert audit.resource_id == session.id
    assert audit.publisher_id == shared_exit.id
    assert audit.client_ip == "203.0.113.8"
    assert sentinel_psk not in audit.detail
    assert json.loads(audit.detail) == {
        "old": {"group_id": old_group.id, "exit_node_id": None, "mode": "split"},
        "new": {"group_id": new_group.id, "exit_node_id": shared_exit.id, "mode": "vpn"},
    }

    # Repeating the same selection is a no-op and must not add an audit row.
    repeated = await renew_session(
        request=request,
        body=None,
        group_id=new_group.id,
        exit_node_id=shared_exit.id,
        db=db_session,
        current_user={"sub": user.id},
    )
    assert repeated.session_id == session.id
    audit_count = len((await db_session.execute(
        select(AccessLog).where(AccessLog.action == "session_selection_changed")
    )).scalars().all())
    assert audit_count == 1

    # An inaccessible group/exit combination is rejected before mutation or audit.
    with pytest.raises(HTTPException) as exc_info:
        await renew_session(
            request=request,
            body=None,
            group_id=new_group.id,
            exit_node_id=inaccessible_exit.id,
            db=db_session,
            current_user={"sub": user.id},
        )
    assert exc_info.value.status_code == 403
    await db_session.refresh(session)
    assert session.selected_group_id == new_group.id
    assert session.exit_node_publisher_id == shared_exit.id
    audit_count = len((await db_session.execute(
        select(AccessLog).where(AccessLog.action == "session_selection_changed")
    )).scalars().all())
    assert audit_count == 1
