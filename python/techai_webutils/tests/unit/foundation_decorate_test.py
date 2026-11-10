"""Tests for ``foundation.decorate``: the layers.

Tiers and config-built Deps (Go ``foundation/decorate``).
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock

import pytest
from botocore.exceptions import ClientError

from techai_webutils.clients.client import tier as client_tier
from techai_webutils.controllers.handler import tier as controller_tier
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.errors.errors import (
    AppError,
    AppTimeoutError,
    ConflictError,
    ErrorCode,
    ForbiddenError,
    InternalError,
    UnavailableError,
)
from techai_webutils.core.interfaces.bulkhead import Bulkhead
from techai_webutils.core.interfaces.circuit_breaker import CircuitBreakerInterface
from techai_webutils.core.interfaces.rate_limiter import RateLimiter
from techai_webutils.core.interfaces.retrier import Retrier
from techai_webutils.core.interfaces.tracer import SpanKind
from techai_webutils.foundation.config import decoration
from techai_webutils.foundation.decorate import (
    Collaborators,
    Decorations,
    Deps,
    Layer,
    OpMiddleware,
    Subject,
    Tier,
    chain,
    exec_op,
    new_auth,
    new_bulkhead,
    new_circuit_breaker,
    new_logging,
    new_metrics,
    new_rate_limit,
    new_recovery,
    new_retry,
    new_timeout,
    new_tracing,
)
from techai_webutils.foundation.resilience.bulkhead import BulkheadFullError
from techai_webutils.foundation.resilience.circuit_breaker import CircuitOpenError
from techai_webutils.repos.repository import tier as repository_tier
from techai_webutils.services.service import tier as service_tier

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from conftest import Observed

_EXPECTED_EXEC_OP = 2
_EXPECTED_EXEC_OP_3 = 3
_EXPECTED_EXEC = 5


async def _run(op: Callable[[], Awaitable[object]]) -> object:
    """Run ``op`` once (a retrier's or bulkhead's pass-through).

    Args:
        op: The operation to run.

    Returns:
        What ``op`` returns.

    """
    return await op()


def _recorder(calls: list[str], label: str) -> OpMiddleware:
    """Return a middleware that records ``label`` on entry, then runs the operation.

    Args:
        calls: Receives each layer's label as it runs.
        label: The layer's label.

    Returns:
        A middleware that records ``label`` on entry, then runs the operation.

    """

    class _Mw:
        @staticmethod
        async def wrap_op(op: str, next_: Callable[[], Awaitable[object]]) -> object:
            del op
            calls.append(label)
            return await next_()

    return _Mw()


class TestLayers:
    """Each layer's behavior."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_chain_runs_outermost_first() -> None:
        """A chain runs its middlewares first-outermost and returns the op's result.

        Why this test is important:
          - The tier order is only meaningful if chain honors it.

        What it tests:
          - ``chain(a, b)`` enters a then b; an empty chain runs the op.
        """
        calls: list[str] = []
        mw = chain(_recorder(calls, "a"), _recorder(calls, "b"))

        assert await exec_op(mw, "Get", AsyncMock(return_value=1)) == 1
        assert calls == ["a", "b"]
        assert (
            await exec_op(chain(), "Get", AsyncMock(return_value=2)) == _EXPECTED_EXEC_OP
        )

    @staticmethod
    @pytest.mark.asyncio
    async def test_recovery_codes_unexpected_errors() -> None:
        """Recovery codes an unexpected exception INTERNAL and logs it.

        A coded one passes.

        Why this test is important:
          - No uncoded exception may escape a tier.

        What it tests:
          - ``RuntimeError`` → ``InternalError`` (logged with the tier's fields);
            ``ConflictError`` passes unchanged; without a logger it still codes.
        """
        logger = MagicMock()
        mw = new_recovery(logger, "service", "projects")

        with pytest.raises(InternalError):
            await exec_op(mw, "Archive", AsyncMock(side_effect=RuntimeError("boom")))
        assert cast(
            "object",
            cast("MagicMock", cast("MagicMock", logger.exception).call_args).kwargs,
        ) == {
            "service": "projects",
            "operation": "Archive",
        }
        with pytest.raises(ConflictError):
            await exec_op(mw, "Archive", AsyncMock(side_effect=ConflictError("dup")))
        with pytest.raises(InternalError):
            await exec_op(
                new_recovery(None, "s", "p"), "A", AsyncMock(side_effect=KeyError())
            )

    @staticmethod
    @pytest.mark.asyncio
    async def test_observability_layers(observed: Observed) -> None:
        """Tracing, metrics and logging name everything ``<tier>``/``<name>.<op>``.

        Why this test is important:
          - Every tier must label its spans, series and lines the same way (Go parity).

        What it tests:
          - The span is ``projects.Get`` of the given kind; the error is recorded on it,
            counted under ``service_errors_total`` and logged as ``service.Get failed``.

        Args:
            observed: Mocked observability collaborators that record the spans.

        """
        obs = observed.observability
        assert obs.tracer is not None
        assert obs.metrics is not None
        assert obs.logger is not None
        mw = chain(
            new_tracing(obs.tracer, "service", "projects", SpanKind.CLIENT),
            new_metrics(obs.metrics, "service", "projects"),
            new_logging(obs.logger, "service", "projects"),
        )

        with pytest.raises(ConflictError):
            await exec_op(mw, "Get", AsyncMock(side_effect=ConflictError("dup")))

        assert observed.spans == [
            (
                "projects.Get",
                SpanKind.CLIENT,
                {"service.name": "projects", "service.operation": "Get"},
            )
        ]
        assert len(observed.span_errors) == 1
        labels = {"service": "projects", "operation": "Get"}
        observed.instruments["service_errors_total"].inc.assert_called_once_with(
            1.0, **labels
        )
        observed.instruments["service_operations_total"].inc.assert_called_once_with(
            1.0, **labels
        )
        observed.instruments[
            "service_operation_duration_seconds"
        ].observe.assert_called_once()
        assert observed.logger.debug.call_args.args[0] == "service.Get failed"
        observed.instruments["service_throttled_total"].inc.assert_not_called()

    @staticmethod
    @pytest.mark.asyncio
    async def test_metrics_counts_a_throttle_apart_from_other_errors(
        observed: Observed,
    ) -> None:
        """A throttled attempt is counted under ``<tier>_throttled_total`` as well as.

        As an error.

        Why this test is important:
          - The quota alerts need throttles apart from every other failure;
            ``<tier>_errors_total`` alone cannot tell an S3 ``SlowDown`` from a bad
            request.

        What it tests:
          - A ``SlowDown`` from the operation increments both ``clients_errors_total`` and
            ``clients_throttled_total`` once, under the same ``(clients, operation)``
            labels.

        Args:
            observed: Mocked observability collaborators that record the spans.

        """
        obs = observed.observability
        assert obs.metrics is not None
        mw = new_metrics(obs.metrics, "clients", "object_store")
        slow_down = ClientError(
            {
                "Error": {"Code": "SlowDown"},
                "ResponseMetadata": {
                    "RequestId": "",
                    "HostId": "",
                    "HTTPStatusCode": 503,
                    "HTTPHeaders": {},
                    "RetryAttempts": 0,
                },
            },
            "PutObject",
        )

        with pytest.raises(ClientError):
            await exec_op(mw, "Put", AsyncMock(side_effect=slow_down))

        labels = {"clients": "object_store", "operation": "Put"}
        observed.instruments["clients_errors_total"].inc.assert_called_once_with(
            1.0, **labels
        )
        observed.instruments["clients_throttled_total"].inc.assert_called_once_with(
            1.0, **labels
        )

    @staticmethod
    @pytest.mark.asyncio
    async def test_guards() -> None:
        """Auth, timeout and the rate limit reject with their codes.

        Why this test is important:
          - Each rejection must carry the code its transport status derives from.

        What it tests:
          - A refused ``projects.Delete`` → ``ForbiddenError`` naming the action; a slow
            op → ``AppTimeoutError``; a refusing limiter → RESOURCE_EXHAUSTED; an allowed
            op runs.
        """
        auth = AsyncMock(return_value=False)
        with pytest.raises(ForbiddenError, match=r"projects\.Delete"):
            await exec_op(new_auth("projects", auth), "Delete", AsyncMock())
        auth.assert_awaited_once_with("projects.Delete")

        async def slow() -> None:
            """Run past the guard's deadline."""
            await asyncio.sleep(1)

        with pytest.raises(AppTimeoutError):
            await exec_op(new_timeout(0.01), "Get", slow)

        limiter = MagicMock(spec=RateLimiter)
        cast("MagicMock", limiter.allow).side_effect = [False, True]
        with pytest.raises(AppError) as caught:
            await exec_op(new_rate_limit(limiter), "Get", AsyncMock())
        assert caught.value.code is ErrorCode.RESOURCE_EXHAUSTED
        assert (
            await exec_op(new_rate_limit(limiter), "Get", AsyncMock(return_value=3))
            == _EXPECTED_EXEC_OP_3
        )

    @staticmethod
    @pytest.mark.asyncio
    async def test_resilience_layers() -> None:
        """Retry delegates to the retrier; an open breaker is UNAVAILABLE.

        A full bulkhead sheds.

        Why this test is important:
          - Each resilience layer's failure must be a coded, classifiable error.

        What it tests:
          - The retrier runs the op; ``CircuitOpenError`` → ``UnavailableError``; a
            waiting bulkhead runs the op through ``execute``; a shedding one turns
            ``BulkheadFullError`` into RESOURCE_EXHAUSTED.
        """
        retrier = MagicMock(spec=Retrier)
        retrier.retry = AsyncMock(side_effect=_run)
        assert (
            await exec_op(new_retry(retrier), "Get", AsyncMock(return_value="ok")) == "ok"
        )

        breaker = MagicMock(spec=CircuitBreakerInterface)
        cast("MagicMock", breaker.__enter__).side_effect = CircuitOpenError()
        with pytest.raises(UnavailableError, match="projects"):
            await exec_op(new_circuit_breaker(breaker, "projects"), "Get", AsyncMock())

        bulkhead = MagicMock(spec=Bulkhead)
        bulkhead.execute = AsyncMock(side_effect=_run)
        bulkhead.try_execute = AsyncMock(side_effect=BulkheadFullError("full"))
        assert (
            await exec_op(
                new_bulkhead(bulkhead, shed=False), "Get", AsyncMock(return_value=1)
            )
            == 1
        )
        with pytest.raises(AppError) as caught:
            await exec_op(new_bulkhead(bulkhead, shed=True), "Get", AsyncMock())
        assert caught.value.code is ErrorCode.RESOURCE_EXHAUSTED


class TestTier:
    """Tiers, subjects and Deps."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_absent_collaborators_leave_their_layers_out() -> None:
        """A layer without its collaborator is not built; recovery needs none.

        Why this test is important:
          - The graph must boot with zero infrastructure (stub-default).

        What it tests:
          - Every layer but recovery is None for empty Deps; recovery is None with
            ``skip_recovery``; a plain Subject runs the op undecorated.
        """
        tier = Tier("service", tuple(Layer))
        empty = Deps()

        built = {layer: tier.middleware(layer, "p", empty) for layer in Layer}
        assert [layer for layer, mw in built.items() if mw is not None] == [
            Layer.RECOVERY
        ]
        assert tier.middleware(Layer.RECOVERY, "p", Deps(skip_recovery=True)) is None
        plain = Subject.plain("p")
        assert plain.name == "p"
        assert await plain.exec("Get", AsyncMock(return_value=5)) == _EXPECTED_EXEC

    @staticmethod
    def test_deps_helpers_drop_one_collaborator() -> None:
        """``without_retry`` and ``without_timeout`` drop only their layer.

        Why this test is important:
          - A create must not retry and a stream must not time out, while keeping every
            other layer.

        What it tests:
          - Each helper clears its field and keeps the rest.
        """
        retrier = MagicMock(spec=Retrier)
        deps = Deps(retrier=retrier, timeout=1.0)

        assert deps.without_retry().retrier is None
        assert deps.without_retry().timeout == pytest.approx(1.0)
        assert deps.without_timeout().timeout == pytest.approx(0.0)
        assert deps.without_timeout().retrier is retrier


class TestDecorations:
    """Deps built from the decoration config."""

    @staticmethod
    def test_no_config_decorates_nothing() -> None:
        """``Decorations()`` gives empty Deps, so only recovery runs.

        Why this test is important:
          - A component built without config must behave as undecorated code.

        What it tests:
          - ``deps`` returns ``Deps()`` for any tier and name.
        """
        assert Decorations().deps(Tier("service", ()), "x") == Deps()

    @staticmethod
    def test_default_config_builds_each_tiers_layers(observed: Observed) -> None:
        """The default config switches each tier's layers like Go's ``DefaultConfig``.

        Why this test is important:
          - Today's behavior must hold with no decoration section at all.

        What it tests:
          - A repository gets retry and a breaker but no observability; a client gets
            observability; the guarded ``storage`` client gets a 30 s timeout and a
            bulkhead; an unknown tier raises ``AppValueError``.

        Args:
            observed: Mocked observability collaborators that record the spans.

        """
        decorations = Decorations(
            decoration.default_config(),
            Collaborators(observability=observed.observability),
        )

        repo = decorations.deps(repository_tier(), "users")
        assert repo.retrier is not None
        assert repo.breaker is not None
        assert repo.observability.tracer is None
        client = decorations.deps(client_tier(), "search")
        assert client.observability.tracer is observed.observability.tracer
        storage = decorations.deps(client_tier(), "storage")
        assert storage.timeout == pytest.approx(30.0)
        assert storage.bulkhead is not None
        assert storage.shed_load is False
        assert decorations.observability is observed.observability
        with pytest.raises(AppValueError, match="no config for tier"):
            _ = decorations.deps(Tier("gateway", ()), "x")

    @staticmethod
    def test_load_reads_the_section_with_the_collaborators(observed: Observed) -> None:
        """``Decorations.load`` builds Decorations.

        From a config loader (Go ``LoadDecorations``).

        Why this test is important:
          - Every composition root turns the decoration section plus its observability
            into Decorations; one engine call keeps each app's wiring to one provider.

        What it tests:
          - An absent section yields the defaults with the collaborators attached (a
            client is traced); a section turning a client's tracing off is applied to that
            client.

        Args:
            observed: Mocked observability collaborators that record the spans.

        """
        collab = Collaborators(observability=observed.observability)
        absent = MagicMock()
        cast("MagicMock", absent.get).return_value = None
        search = Decorations.load(absent, collab).deps(client_tier(), "search")
        assert search.observability.tracer is observed.observability.tracer

        quiet = MagicMock()
        cast("MagicMock", quiet.get).return_value = {
            "clients": {"overrides": {"search": {"tracing": False}}}
        }
        search = Decorations.load(quiet, collab).deps(client_tier(), "search")
        assert search.observability.tracer is None
        assert search.observability.logger is observed.observability.logger

    @staticmethod
    @pytest.mark.asyncio
    async def test_recovery_logs_a_panic_with_logging_off(observed: Observed) -> None:
        """Recovery keeps the collaborators' logger when the logging layer is off.

        Why this test is important:
          - Services are not logged by default; an unexpected exception there would
            otherwise become INTERNAL with its traceback thrown away.

        What it tests:
          - A default-config service has no logging layer, yet a failing op is logged with
            ``exception`` and raises ``InternalError``; with recovery off no panic logger
            is set.

        Args:
            observed: Mocked observability collaborators that record the spans.

        """
        decorations = Decorations(
            decoration.default_config(),
            Collaborators(observability=observed.observability),
        )
        deps = decorations.deps(service_tier(), "users")
        assert deps.observability.logger is None

        async def boom() -> None:
            """Panic.

            Raises:
                RuntimeError: Always.

            """
            await asyncio.sleep(0)
            msg = "boom"
            raise RuntimeError(msg)

        with pytest.raises(InternalError):
            await service_tier().subject("users", deps).exec("provision", boom)
        observed.logger.exception.assert_called_once()
        assert observed.logger.exception.call_args.args == ("service panic recovered",)
        quiet = decoration.from_mapping({
            "services": {"overrides": {"users": {"recovery": False}}}
        })
        assert Decorations(quiet).deps(service_tier(), "users").panic_logger is None

    @staticmethod
    def test_overrides_build_every_primitive_and_name_a_bad_tuning() -> None:
        """An override turns on the rate limit and a shedding adaptive bulkhead.

        A bad tuning is named.

        Why this test is important:
          - Every layer must be switchable and tunable per component, and a bad value must
            point at the component and layer that carry it.

        What it tests:
          - ``api`` gets a limiter, an adaptive bulkhead and shed load; an unknown
            controller bulkhead kind and a negative client retry raise ``AppValueError``
            naming ``<name>.<layer>``.
        """
        config = decoration.from_mapping({
            "controllers": {
                "overrides": {
                    "api": {
                        "rate_limit": {"enabled": True},
                        "bulkhead": {"enabled": True, "shed": True, "kind": "adaptive"},
                    },
                    "odd": {"bulkhead": {"enabled": True, "kind": "lifo"}},
                }
            },
            "clients": {
                "overrides": {"neg": {"retry": {"enabled": True, "max_retries": -1}}}
            },
        })
        decorations = Decorations(config)
        controller = controller_tier()

        api = decorations.deps(controller, "api")
        assert api.rate_limiter is not None
        assert api.bulkhead is not None
        assert api.shed_load is True
        with pytest.raises(AppValueError, match=r"odd\.bulkhead"):
            _ = decorations.deps(controller, "odd")
        with pytest.raises(AppValueError, match=r"neg\.retry"):
            _ = decorations.deps(client_tier(), "neg")
