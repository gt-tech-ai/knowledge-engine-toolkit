"""Unit tests for Python core domain types.

Tests all shared types for correctness and Go parity: ID, Option, Page,
PageRequest, SortOrder, SortField, and Timestamps.
"""

import dataclasses
from datetime import UTC, datetime

import pytest

from techai_webutils.core.domain_types.types import (
    ID,
    Option,
    Page,
    PageRequest,
    SortField,
    SortOrder,
    Timestamps,
    none,
    some,
)


class TestID:
    """Test suite for ID typed string."""

    def test_is_empty_when_empty(self) -> None:
        """Test that is_empty reports True for a zero-value ID.

        **Why this test is important:**
          - An empty ID is the library's "unset/missing entity" sentinel; code branches
            on it instead of comparing against magic strings scattered across services
          - If is_empty mis-reported an empty ID as populated, downstream lookups would
            run against an absent key and silently return wrong-tenant or no data

        **What it tests:**
          - ID("").is_empty() returns True for the empty-string identifier
        """
        assert ID("").is_empty() is True

    def test_is_empty_when_populated(self) -> None:
        """Test that is_empty reports False for a populated ID.

        **Why this test is important:**
          - A real identifier must never be mistaken for "unset", or valid entities would
            be skipped as if they did not exist
          - Guards the inverse of the empty case so the sentinel check is correct in both
            directions

        **What it tests:**
          - ID("abc-123").is_empty() returns False for a non-empty identifier
        """
        assert ID("abc-123").is_empty() is False

    def test_string_representation(self) -> None:
        """Test that an ID renders as its underlying string value.

        **Why this test is important:**
          - IDs travel through logs, URLs, gRPC metadata, and SQL parameters as plain
            strings; any wrapping/prefix in str() would corrupt every serialized form
          - Confirms the typed wrapper adds compile-time safety without changing the wire
            representation it shares with its Go counterpart

        **What it tests:**
          - str(ID("user-1")) equals the raw "user-1" with no decoration
        """
        assert str(ID("user-1")) == "user-1"

    def test_is_subclass_of_str(self) -> None:
        """Test that ID is a genuine str subclass.

        **Why this test is important:**
          - ID is a str subclass precisely so it can be passed anywhere a plain string is
            expected (dict keys, format strings, DB drivers) with zero conversion
          - If it were not an instance of str, isinstance-based and duck-typed string code
            paths across the stack would reject valid identifiers

        **What it tests:**
          - isinstance(ID("x"), str) is True, proving ID transparently behaves as a string
        """
        assert isinstance(ID("x"), str)


class TestOption:
    """Test suite for Option monad."""

    def test_some_contains_value(self) -> None:
        """Test that some(v) yields a present Option carrying that value.

        **Why this test is important:**
          - Option is the library's explicit "value may be absent" type, mirroring Go's
            (value, ok) idiom; the get() contract is what lets callers branch safely
            instead of relying on None ambiguity (where None is itself a valid value)
          - If some() returned valid=False or dropped the value, every present-value
            consumer would treat real data as missing

        **What it tests:**
          - some(42).get() returns (42, True): the value is carried and the present flag set
        """
        opt = some(42)
        value, valid = opt.get()
        assert valid is True
        assert value == 42

    def test_none_is_empty(self) -> None:
        """Test that none() yields an absent Option with no value.

        **Why this test is important:**
          - none() is the canonical "no value" producer; callers depend on valid=False to
            take the absence branch rather than misreading a default as real data
          - Distinguishes a deliberately empty Option from a present Option that happens to
            wrap None, which is the whole reason Option exists over bare None

        **What it tests:**
          - none().get() returns (None, False): no value and the present flag cleared
        """
        opt: Option[int] = none()
        value, valid = opt.get()
        assert valid is False
        assert value is None

    def test_or_else_returns_value_when_present(self) -> None:
        """Test that or_else returns the contained value when the Option is present.

        **Why this test is important:**
          - or_else is the ergonomic unwrap-with-default that keeps call sites free of
            manual get()/None checks; on the present path it must never substitute the
            fallback or real values would be silently overwritten by defaults

        **What it tests:**
          - some("hello").or_else("default") returns "hello", ignoring the fallback
        """
        opt = some("hello")
        assert opt.or_else("default") == "hello"

    def test_or_else_returns_fallback_when_empty(self) -> None:
        """Test that or_else returns the fallback when the Option is empty.

        **Why this test is important:**
          - The absence path of or_else is the safety net that supplies a sane default
            instead of propagating None into code that cannot handle it
          - A broken fallback path would leak None past the unwrap, defeating Option's
            purpose of eliminating downstream None-handling

        **What it tests:**
          - none().or_else("default") returns the "default" fallback for an empty Option
        """
        opt: Option[str] = none()
        assert opt.or_else("default") == "default"

    def test_valid_property(self) -> None:
        """Test that the valid property reflects presence for both Option states.

        **Why this test is important:**
          - valid is the read-only presence flag callers inspect without destructuring via
            get(); it must agree with the some()/none() constructors or guards built on it
            would take the wrong branch
          - Covers both states in one assertion to lock the property to its constructors

        **What it tests:**
          - some(1).valid is True and none().valid is False
        """
        assert some(1).valid is True
        assert none().valid is False

    def test_frozen(self) -> None:
        """Test that an Option rejects mutation of its wrapped value.

        **Why this test is important:**
          - Option is a frozen dataclass so a present/absent decision cannot be flipped
            after construction; immutability makes it safe to share across goroutines'
            Python analogue (async tasks, caches) without defensive copies
          - If assignment silently succeeded, a cached "present" Option could be mutated to
            "absent" under another caller, producing data-dependent heisenbugs

        **What it tests:**
          - Assigning to an attribute raises FrozenInstanceError, leaving the value intact
        """
        opt = some(42)
        with pytest.raises(dataclasses.FrozenInstanceError):
            opt.valid = False
        assert opt.get() == (42, True)


class TestPage:
    """Test suite for Page pagination container."""

    def test_has_more_when_more_pages(self) -> None:
        """Test that has_more is True when consumed rows are fewer than the total.

        **Why this test is important:**
          - has_more drives "load more"/next-page controls in the UI and pagination loops in
            clients; a false negative here would strand records that the user can never reach
          - Exercises the core arithmetic (page_number * page_size < total) on a mid-stream
            page where more results genuinely remain

        **What it tests:**
          - With 2 of 10 rows seen on page 1 (size 2), has_more() returns True
        """
        page: Page[str] = Page(
            items=["a", "b"],
            total=10,
            page_size=2,
            page_number=1,
        )
        assert page.has_more() is True

    def test_has_more_when_last_page(self) -> None:
        """Test that has_more is False once the final page has been reached.

        **Why this test is important:**
          - A false positive at the end of a result set causes clients to request a
            non-existent page, wasting round trips and risking an infinite pagination loop
          - Confirms the boundary where consumed rows exactly equal the total terminates
            iteration

        **What it tests:**
          - With 4 of 4 rows seen on page 2 (size 2), has_more() returns False
        """
        page: Page[str] = Page(
            items=["a", "b"],
            total=4,
            page_size=2,
            page_number=2,
        )
        assert page.has_more() is False

    def test_has_more_when_exact_fit(self) -> None:
        """Test the off-by-one boundary where a partial page consumes the whole result set.

        **Why this test is important:**
          - The page_number * page_size product (1 * 2 = 2) equals total here, so a `<` vs
            `<=` slip in has_more would flip the answer — this is the classic off-by-one that
            pagination bugs hide in
          - Verifies a short final page (1 item) is still correctly recognized as the last

        **What it tests:**
          - With 1 item, total 2, size 2 on page 1, has_more() returns False (2 < 2 is False)
        """
        page: Page[str] = Page(
            items=["a"],
            total=2,
            page_size=2,
            page_number=1,
        )
        assert page.has_more() is False

    def test_empty_page(self) -> None:
        """Test that a default-constructed Page is a safe, well-formed empty result.

        **Why this test is important:**
          - "No results" is a routine outcome (empty search, filtered-out tenant data); the
            zero value must be usable without NoneType errors or a phantom next page
          - The mutable items default must be a fresh list per instance — a shared default
            would leak rows between unrelated queries

        **What it tests:**
          - Page() yields items == [], total == 0, and has_more() == False
        """
        page: Page[str] = Page()
        assert page.items == []
        assert page.total == 0
        assert page.has_more() is False

    def test_next_cursor(self) -> None:
        """Test that an opaque next_cursor token is stored and returned verbatim.

        **Why this test is important:**
          - Cursor-based pagination passes this token back to the server to fetch the
            following page; any mangling would break the continuation and lose results
          - The cursor is opaque to the client, so the type must preserve it byte-for-byte
            rather than parsing or normalizing it

        **What it tests:**
          - Page(next_cursor="abc123").next_cursor returns the exact "abc123" token
        """
        page: Page[str] = Page(next_cursor="abc123")
        assert page.next_cursor == "abc123"


class TestPageRequest:
    """Test suite for PageRequest."""

    def test_defaults(self) -> None:
        """Test that PageRequest supplies sane pagination defaults.

        **Why this test is important:**
          - Callers that omit pagination params rely on these defaults to bound query result
            size; a missing or zero page_size default would return unbounded result sets and
            risk DoS-ing the database and the client
          - The defaults form an API contract shared with the Go counterpart, so a drift here
            would mean Go and Python clients page differently

        **What it tests:**
          - PageRequest() defaults to page_size 20, page_number 1, and an empty cursor
        """
        req = PageRequest()
        assert req.page_size == 20
        assert req.page_number == 1
        assert not req.cursor

    def test_custom_values(self) -> None:
        """Test that explicit pagination arguments override the defaults.

        **Why this test is important:**
          - Clients control page size and offset to tune throughput vs. latency; the request
            must honor caller-supplied values exactly rather than clamping to defaults
          - Confirms all three fields are independently settable so cursor and offset paging
            can coexist

        **What it tests:**
          - PageRequest(page_size=50, page_number=3, cursor="xyz") stores each value as given
        """
        req = PageRequest(page_size=50, page_number=3, cursor="xyz")
        assert req.page_size == 50
        assert req.page_number == 3
        assert req.cursor == "xyz"


class TestSortOrder:
    """Test suite for SortOrder enum."""

    def test_values(self) -> None:
        """Test that SortOrder members serialize to the exact wire strings.

        **Why this test is important:**
          - "asc"/"desc" are the literal tokens sent to the API and matched by the Go side
            and SQL ORDER BY mapping; renaming a member without updating the value would
            silently sort in the wrong direction or be rejected as an unknown order
          - Pins the enum values as a cross-service contract, not an internal detail

        **What it tests:**
          - SortOrder.ASC equals "asc" and SortOrder.DESC equals "desc"
        """
        assert SortOrder.ASC == "asc"
        assert SortOrder.DESC == "desc"

    def test_is_str_enum(self) -> None:
        """Test that SortOrder members are genuine strings.

        **Why this test is important:**
          - As a StrEnum, members must drop straight into JSON payloads, query strings, and
            f-strings without an explicit .value access; if it were a plain Enum, serializers
            would emit "SortOrder.ASC" instead of "asc"
          - Guarantees the enum is interchangeable with the raw string everywhere a sort
            direction is passed across the wire

        **What it tests:**
          - isinstance(SortOrder.ASC, str) is True
        """
        assert isinstance(SortOrder.ASC, str)


class TestSortField:
    """Test suite for SortField."""

    def test_default_order(self) -> None:
        """Test that a SortField defaults to ascending order.

        **Why this test is important:**
          - Ascending is the conventional, least-surprising default; callers that name a
            field without a direction expect A→Z / oldest-first ordering
          - A wrong default would reverse list results for every caller that relies on the
            implicit order, a subtle and widespread UX bug

        **What it tests:**
          - SortField(field="name") records field "name" and order SortOrder.ASC
        """
        sf = SortField(field="name")
        assert sf.field == "name"
        assert sf.order == SortOrder.ASC

    def test_desc_order(self) -> None:
        """Test that an explicit descending order is honored.

        **Why this test is important:**
          - Descending sorts power the common "newest first" views (recent documents,
            activity feeds); the field must carry the caller's chosen direction unchanged
          - Confirms the order argument actually overrides the ascending default rather than
            being ignored

        **What it tests:**
          - SortField(field="created_at", order=SortOrder.DESC) reports SortOrder.DESC
        """
        sf = SortField(field="created_at", order=SortOrder.DESC)
        assert sf.order == SortOrder.DESC

    def test_frozen(self) -> None:
        """Test that a SortField rejects mutation after construction.

        **Why this test is important:**
          - SortField is frozen so a sort spec can be safely reused and cached across
            requests without one caller's mutation bleeding into another's query ordering
          - If the field were reassignable, a shared sort descriptor could be flipped
            mid-flight, sorting some results by an unintended column

        **What it tests:**
          - Assigning to sf.field raises AttributeError (frozen dataclass)
        """
        sf = SortField(field="name")
        try:
            sf.field = "other"  # type: ignore[misc]
            msg = "Should have raised FrozenInstanceError"
            raise AssertionError(msg)
        except AttributeError:
            pass


class TestTimestamps:
    """Test suite for Timestamps with soft delete."""

    def test_not_deleted_by_default(self) -> None:
        """Test that a freshly created entity is not flagged as deleted.

        **Why this test is important:**
          - Soft-delete filtering (WHERE deleted_at IS NULL) is what keeps "deleted" rows out
            of every query; a new entity that defaulted to deleted would vanish from all
            listings the instant it was created
          - Establishes the live baseline that test_soft_delete then transitions from

        **What it tests:**
          - Timestamps() reports is_deleted False and deleted_at None
        """
        ts = Timestamps()
        assert ts.is_deleted is False
        assert ts.deleted_at is None

    def test_soft_delete(self) -> None:
        """Test that soft_delete marks the entity deleted by stamping deleted_at.

        **Why this test is important:**
          - Soft delete is how a repository removes records while preserving them for audit
            and recovery; if it failed to set deleted_at, "deleted" data would keep surfacing
            in results — a data-leak and compliance risk
          - is_deleted is derived from deleted_at, so this verifies the flag and the timestamp
            move together

        **What it tests:**
          - After soft_delete(), is_deleted is True and deleted_at is populated (not None)
        """
        ts = Timestamps()
        ts.soft_delete()
        assert ts.is_deleted is True
        assert ts.deleted_at is not None

    def test_created_at_and_updated_at_are_set(self) -> None:
        """Test that creation auto-stamps created_at and updated_at to now (UTC).

        **Why this test is important:**
          - Audit ordering, cache invalidation, and "last modified" displays all depend on
            these timestamps being populated automatically; a None or unset value would break
            chronological sorting and staleness checks
          - The default_factory must produce timezone-aware UTC instants — a naive datetime
            would compare incorrectly across services and break the `>= before` invariant

        **What it tests:**
          - created_at and updated_at are both at or after a UTC instant captured pre-construction
        """
        before = datetime.now(tz=UTC)
        ts = Timestamps()
        assert ts.created_at >= before
        assert ts.updated_at >= before
