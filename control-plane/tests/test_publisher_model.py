"""Tests for Publisher model — virtual_cidr and publisher_index fields."""

import pytest
import pytest_asyncio
from sqlalchemy.ext.asyncio import create_async_engine, AsyncSession, async_sessionmaker
from sqlalchemy import select

from app.database import Base
from app.models.publisher import Publisher


@pytest_asyncio.fixture
async def db_session():
    """Create an in-memory SQLite async session for testing."""
    engine = create_async_engine("sqlite+aiosqlite:///:memory:", echo=False)
    async with engine.begin() as conn:
        await conn.run_sync(Base.metadata.create_all)

    session_factory = async_sessionmaker(engine, class_=AsyncSession, expire_on_commit=False)
    async with session_factory() as session:
        yield session

    await engine.dispose()


class TestComputeVirtualCidr:
    """Test the static compute_virtual_cidr method."""

    def test_index_1(self):
        assert Publisher.compute_virtual_cidr(1) == "10.252.1.0/24"

    def test_index_5(self):
        assert Publisher.compute_virtual_cidr(5) == "10.252.5.0/24"

    def test_index_255(self):
        assert Publisher.compute_virtual_cidr(255) == "10.252.255.0/24"

    def test_index_0(self):
        assert Publisher.compute_virtual_cidr(0) == "10.252.0.0/24"


class TestNextPublisherIndex:
    """Test auto-assignment of publisher_index."""

    @pytest.mark.asyncio
    async def test_first_publisher_gets_index_1(self, db_session):
        index = await Publisher.next_publisher_index(db_session)
        assert index == 1

    @pytest.mark.asyncio
    async def test_sequential_indices(self, db_session):
        # Create first publisher with index 1
        pub1 = Publisher(name="pub1", publisher_index=1, virtual_cidr="10.252.1.0/24")
        db_session.add(pub1)
        await db_session.flush()

        # Next should be 2
        index = await Publisher.next_publisher_index(db_session)
        assert index == 2

    @pytest.mark.asyncio
    async def test_gap_handling(self, db_session):
        """next_publisher_index uses max+1, not gap-filling."""
        pub1 = Publisher(name="pub1", publisher_index=1, virtual_cidr="10.252.1.0/24")
        pub3 = Publisher(name="pub3", publisher_index=3, virtual_cidr="10.252.3.0/24")
        db_session.add_all([pub1, pub3])
        await db_session.flush()

        # Should return 4 (max+1), not 2 (gap)
        index = await Publisher.next_publisher_index(db_session)
        assert index == 4


class TestPublisherCreation:
    """Test that publisher_index and virtual_cidr are stored correctly."""

    @pytest.mark.asyncio
    async def test_create_publisher_with_index_and_cidr(self, db_session):
        index = await Publisher.next_publisher_index(db_session)
        virtual_cidr = Publisher.compute_virtual_cidr(index)

        pub = Publisher(
            name="test-publisher",
            publisher_index=index,
            virtual_cidr=virtual_cidr,
            exposed_cidrs=["10.0.0.0/24"],
        )
        db_session.add(pub)
        await db_session.flush()
        await db_session.refresh(pub)

        assert pub.publisher_index == 1
        assert pub.virtual_cidr == "10.252.1.0/24"

    @pytest.mark.asyncio
    async def test_unique_constraint_on_publisher_index(self, db_session):
        """Two publishers cannot share the same publisher_index."""
        from sqlalchemy.exc import IntegrityError

        pub1 = Publisher(name="pub1", publisher_index=1, virtual_cidr="10.252.1.0/24")
        pub2 = Publisher(name="pub2", publisher_index=1, virtual_cidr="10.252.1.0/24")
        db_session.add(pub1)
        await db_session.flush()

        db_session.add(pub2)
        with pytest.raises(IntegrityError):
            await db_session.flush()
