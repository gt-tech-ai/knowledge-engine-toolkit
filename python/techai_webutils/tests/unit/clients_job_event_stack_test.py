"""Tests for the Python Job + EventHandler decorator stacks.

Parity with Go's ``go/tests/unit/clients_jobevent_stack_test.go``: the event stack skips a
duplicate and dead-letters a message that still fails after retries; the job stack
runs only on the leader.
"""

from __future__ import annotations

import asyncio

import pytest

from techai_webutils.clients.decorators.event_stack import EventStackDeps, wrap_handler
from techai_webutils.clients.decorators.job_stack import JobStackDeps, wrap_job
from techai_webutils.core.errors.errors import UnavailableError
from techai_webutils.core.interfaces.messaging import Message
from techai_webutils.foundation.resilience.dedup import MemoryDeduplicator
from techai_webutils.foundation.resilience.dlq import (
    DeadLetterQueue,
    StubDeadLetterBackend,
)
from techai_webutils.foundation.resilience.leader import AlwaysLeader


class _NotLeader:
    """A ``LeaderElector`` that never holds leadership (for the leader-gating test)."""

    async def is_leader(self) -> bool:
        """Never the leader."""
        return False


def _message(message_id: str = "m1") -> Message:
    """Build a minimal message (id, topic, payload) for the stack tests."""
    return Message(id=message_id, topic="t", payload=b"x")


@pytest.mark.asyncio
async def test_wrap_handler_dedup_skips_duplicate() -> None:
    """The event stack runs a redelivered message's handler exactly once.

    Why this test is important:
        - Under at-least-once delivery the same message can arrive twice; the dedup layer is the
          guard, and re-running the handler would double-process (e.g. duplicate side effects).

    What it tests:
        - Two handler invocations for the same message id run the inner handler exactly once.
    """
    calls = 0

    async def inner(_msg: Message) -> None:
        await asyncio.sleep(0)
        nonlocal calls
        calls += 1

    handler = wrap_handler(inner, EventStackDeps(dedup=MemoryDeduplicator()))
    msg = _message()

    await handler(msg)
    await handler(msg)

    assert calls == 1, "the handler must run exactly once for a duplicate delivery"


@pytest.mark.asyncio
async def test_wrap_handler_retries_then_dead_letters() -> None:
    """A message that still fails after retries is dead-lettered and acked.

    Why this test is important:
        - A poison message that neither dead-letters nor acks would loop forever; retrying the
          transient failure then routing it to the DLQ is how the consumer makes progress without
          losing the message.

    What it tests:
        - An always-failing (transient) handler is retried up to the configured attempts and then
          recorded in the dead-letter backend, with no exception escaping (the message is acked).
    """
    backend = StubDeadLetterBackend()
    attempts = 0

    async def inner(_msg: Message) -> None:
        await asyncio.sleep(0)
        nonlocal attempts
        attempts += 1
        msg = "downstream down"
        raise UnavailableError(msg)

    handler = wrap_handler(
        inner,
        EventStackDeps(dlq=DeadLetterQueue(backend), retry_max_attempts=2),
    )

    await handler(_message())  # must not raise -- the failure is dead-lettered

    assert attempts == 2, "the transient failure is retried before dead-lettering"
    assert len(backend.letters) == 1
    assert backend.letters[0].id == "m1"


@pytest.mark.asyncio
async def test_wrap_job_leader_gating() -> None:
    """The job stack runs the job only on the leader, so an N-replica schedule fires once.

    Why this test is important:
        - Running a scheduled job on every replica would duplicate its effect (e.g. N reaper
          sweeps); leader election is the guard that makes it fire once.

    What it tests:
        - A non-leader instance skips the job (never runs it); the leader runs it exactly once.
    """
    follower_calls = 0
    leader_calls = 0

    async def follower_job() -> None:
        await asyncio.sleep(0)
        nonlocal follower_calls
        follower_calls += 1

    async def leader_job() -> None:
        await asyncio.sleep(0)
        nonlocal leader_calls
        leader_calls += 1

    await wrap_job(follower_job, JobStackDeps(leader=_NotLeader()))()
    assert follower_calls == 0, "a non-leader must not run the job"

    await wrap_job(leader_job, JobStackDeps(leader=AlwaysLeader()))()
    assert leader_calls == 1, "the leader runs the job once"


@pytest.mark.asyncio
async def test_wrap_handler_open_breaker_fails_fast_without_starting_the_handler() -> (
    None
):
    """With the circuit breaker open, the handler is not even called (no orphaned coroutine).

    Why this test is important:
        - The attempt used to build the handler coroutine before entering the breaker; when the breaker
          was open it raised first, so every message left an un-awaited coroutine behind (a
          "coroutine was never awaited" RuntimeWarning and a leaked frame per message while open).

    What it tests:
        - After one failure opens a threshold-1 breaker, the next message raises CircuitOpenError, the
          handler callable is not invoked again, and no "never awaited" RuntimeWarning is emitted.
    """
    import gc
    import warnings

    from techai_webutils.foundation.resilience.circuit_breaker import (
        CircuitBreaker,
        CircuitOpenError,
    )

    calls = 0

    async def failing() -> None:
        await asyncio.sleep(0)
        msg = "downstream down"
        raise UnavailableError(msg)

    def inner(_msg: Message) -> object:
        nonlocal calls
        calls += 1
        return failing()

    handler = wrap_handler(
        inner,  # type: ignore[arg-type]
        EventStackDeps(
            circuit_breaker=CircuitBreaker(failure_threshold=1, recovery_timeout=60.0)
        ),
    )
    with pytest.raises(UnavailableError):
        await handler(_message("m1"))  # opens the breaker

    with warnings.catch_warnings(record=True) as caught:
        warnings.simplefilter("always")
        with pytest.raises(CircuitOpenError):
            await handler(_message("m2"))
        gc.collect()

    assert calls == 1
    assert not [w for w in caught if "never awaited" in str(w.message)]
