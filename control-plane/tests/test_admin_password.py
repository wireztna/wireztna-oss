import pytest
import pytest_asyncio
from fastapi import HTTPException
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker, create_async_engine

import app.models  # noqa: F401 -- register all metadata and relationship targets
from app.database import Base
from app.models.user import User
from app.routers.users import admin_set_password
from app.routers.oidc_auth import get_oidc_config
from app.schemas.schemas import AdminSetPasswordRequest
from app.services.auth_service import verify_password


@pytest_asyncio.fixture
async def db_session():
    engine = create_async_engine("sqlite+aiosqlite:///:memory:", echo=False)
    async with engine.begin() as conn:
        await conn.run_sync(Base.metadata.create_all)

    session_factory = async_sessionmaker(engine, class_=AsyncSession, expire_on_commit=False)
    async with session_factory() as session:
        yield session

    await engine.dispose()


@pytest.mark.asyncio
async def test_admin_set_password_updates_hash_without_smtp(db_session):
    user = User(
        id="target-user",
        username="target",
        email="target@example.test",
        password_hash=None,
        status="active",
    )
    db_session.add(user)
    await db_session.flush()

    result = await admin_set_password(
        user_id="target-user",
        body=AdminSetPasswordRequest(password="N3wStrongPass!", must_change_password=True),
        db=db_session,
        current_user={"sub": "admin-user", "role": "super_admin", "is_admin": True},
    )

    assert "target" in result["message"]
    await db_session.refresh(user)
    assert user.password_hash is not None
    assert verify_password("N3wStrongPass!", user.password_hash)
    assert user.must_change_password is True


@pytest.mark.asyncio
async def test_admin_set_password_rejects_missing_user(db_session):
    with pytest.raises(HTTPException) as exc_info:
        await admin_set_password(
            user_id="does-not-exist",
            body=AdminSetPasswordRequest(password="N3wStrongPass!"),
            db=db_session,
            current_user={"sub": "admin-user", "role": "super_admin", "is_admin": True},
        )
    assert exc_info.value.status_code == 404


@pytest.mark.asyncio
async def test_admin_set_password_rejects_weak_password(db_session):
    user = User(id="u2", username="u2", email="u2@example.test", status="active")
    db_session.add(user)
    await db_session.flush()

    with pytest.raises(HTTPException) as exc_info:
        await admin_set_password(
            user_id="u2",
            body=AdminSetPasswordRequest(password="short1"),
            db=db_session,
            current_user={"sub": "admin-user", "role": "super_admin", "is_admin": True},
        )
    assert exc_info.value.status_code == 400


@pytest.mark.asyncio
async def test_oidc_config_setup_required_when_no_users(db_session, monkeypatch):
    monkeypatch.setattr("app.routers.oidc_auth.settings.oidc_enabled", False)
    monkeypatch.setattr("app.routers.oidc_auth.settings.smtp_host", "")

    cfg = await get_oidc_config(db=db_session)

    assert cfg.enabled is False
    assert cfg.email_enabled is False
    assert cfg.otp_enabled is False
    assert cfg.setup_required is True


@pytest.mark.asyncio
async def test_oidc_config_email_enabled_and_setup_done_with_user(db_session, monkeypatch):
    db_session.add(User(id="u1", username="u1", email="u1@example.test", status="active"))
    await db_session.flush()

    monkeypatch.setattr("app.routers.oidc_auth.settings.oidc_enabled", False)
    monkeypatch.setattr("app.routers.oidc_auth.settings.smtp_host", "smtp.example.test")

    cfg = await get_oidc_config(db=db_session)

    assert cfg.email_enabled is True
    assert cfg.otp_enabled is True
    assert cfg.setup_required is False
