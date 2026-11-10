"""The pipeline tier (mirrors Go ``pipelines/pipeline``).

``tier()`` orders the tier's layers; ``PipelineBase``, built from the pipeline's contract,
runs each operation through it, and ``PipelineBase.stage`` builds one as a decorated
``Stage`` — one span, metric series and log line per stage — that ``Stage.then`` chains
into a pipeline. ``PipelineBase.fan_out`` runs stages concurrently over one input, and
``PipelineBase.crud`` runs a CRUD service's five roles as the pipeline's operations.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, final, override

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.crud_service import CrudService
from techai_webutils.core.interfaces.pipeline import AsyncPipeline
from techai_webutils.foundation.decorate import Layer, Tier
from techai_webutils.foundation.decorate.roles import CrudOps

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable
    from typing import ClassVar, Literal

    from techai_webutils.foundation.decorate import Decorations, Subject


def tier() -> Tier:
    """Return the pipeline tier, outermost first.

    Recovery, logging, tracing, metrics, timeout.

    Returns:
        The pipeline tier, outermost first: recovery, logging, tracing, metrics.

    """
    return Tier(
        "pipeline",
        (Layer.RECOVERY, Layer.LOGGING, Layer.TRACING, Layer.METRICS, Layer.TIMEOUT),
    )


@final
class Stage[In, Out](AsyncPipeline[In, Out]):
    """A pipeline built from a function; ``then`` chains it with the next stage."""

    def __init__(self, fn: Callable[[In], Awaitable[Out]]) -> None:
        """Bind the stage's function.

        Args:
            fn: The stage's function.

        """
        self._fn = fn

    @override
    async def execute(self, input_data: In) -> Out:
        """Run the stage.

        Args:
            input_data: The stage's input.

        Returns:
            The stage's output.

        """
        return await self._fn(input_data)

    def then[C](self, next_: AsyncPipeline[Out, C] | None) -> Stage[In, C]:
        """Return the pipeline that feeds this stage's output to ``next_``.

        An error from this stage propagates unchanged and ``next_`` does not run; each
        stage keeps its own decorators. A None stage is a wiring bug and raises
        ``AppValueError``.

        Args:
            next_: The next stage; ``None`` returns this one.

        Returns:
            The pipeline that feeds this stage's output to ``next_``.

        Raises:
            AppValueError: Pipeline: then stage must not be None.

        """
        if next_ is None:
            msg = "pipeline: then stage must not be None"
            raise AppValueError(msg)

        async def run(input_data: In) -> C:
            """Run this stage, then ``next_`` on its output.

            Args:
                input_data: The stage's input.

            Returns:
                ``next_``'s output.

            """
            return await next_.execute(await self._fn(input_data))

        return Stage(run)


class PipelineBase:
    """Runs one pipeline's operations through the pipeline tier (Go ``pipeline.Base``).

    Each operation is one method of the pipeline's contract, decorated as
    ``<name>.<method>``; ``stage`` builds an operation as a composable Stage.
    """

    __slots__: ClassVar[tuple[Literal["_subject"]]] = ("_subject",)

    def __init__(self, contract: type, decorations: Decorations, name: str) -> None:
        """Bind the pipeline ``name``: one chain per method of ``contract``.

        Go's ``NewBase[P]``.

        Args:
            contract: The interface whose methods are the operations.
            decorations: Resolves the tier's layers.
            name: The component's name in the decoration config.

        """
        self._subject: Subject = decorations.subject_of(tier(), name, contract)

    @property
    def name(self) -> str:
        """The pipeline's name."""
        return self._subject.name

    async def exec[R](self, op: str, fn: Callable[[], Awaitable[R]]) -> R:
        """Run ``fn`` as the operation ``op`` through the tier.

        Args:
            op: The operation.
            fn: The call.

        Returns:
            What ``fn`` returns.

        """
        return await self._subject.exec(op, fn)

    def stage[In, Out](
        self, op: str, fn: Callable[[In], Awaitable[Out]] | None
    ) -> Stage[In, Out]:
        """Return ``fn`` as the decorated operation ``op``.

        The stage composes with ``then``.

        A None function is a wiring bug and raises ``AppValueError``.

        Args:
            op: The operation.
            fn: The stage's function; ``None`` passes its input through.

        Returns:
            ``fn`` as the decorated operation ``op``.

        Raises:
            AppValueError: Pipeline: stage fn must not be None.

        """
        if fn is None:
            msg = "pipeline: stage fn must not be None"
            raise AppValueError(msg)
        subject = self._subject

        async def run(input_data: In) -> Out:
            """Run the stage's function through the pipeline tier.

            Args:
                input_data: The stage's input.

            Returns:
                The function's output.

            """
            return await subject.exec(op, lambda: fn(input_data))

        return Stage(run)

    def fan_out[In, Out](
        self, op: str, *stages: AsyncPipeline[In, Out]
    ) -> Stage[In, list[Out]]:
        """Return a stage, decorated as ``op``, that runs ``stages`` concurrently.

        The outputs keep the stages' order; the first failure cancels the rest and is
        raised.

        Args:
            op: The operation.
            *stages: The stages run on the same input.

        Returns:
            The decorated operation ``op``.

        """

        async def run(input_data: In) -> list[Out]:
            """Run every stage on the input concurrently.

            Args:
                input_data: The stages' input.

            Returns:
                Each stage's output, in the stages' order.

            """
            if not stages:
                return []
            tasks = [asyncio.ensure_future(s.execute(input_data)) for s in stages]
            try:
                _ = await asyncio.wait(tasks, return_when=asyncio.FIRST_EXCEPTION)
            finally:
                # The first failure (or the caller's cancellation) stops the rest; wait
                # for them to unwind so none outlives the stage.
                for task in tasks:
                    _ = task.cancel()
                _ = await asyncio.wait(tasks)
            # The failed task's own error reaches the caller, its code intact; the
            # cancelled ones only follow from it.
            for task in tasks:
                if not task.cancelled() and task.exception() is not None:
                    _ = task.result()
            return [t.result() for t in tasks]

        return self.stage(op, run)

    def crud[T, P, ID](self, svc: CrudService[T, P, ID]) -> CrudService[T, P, ID]:
        """Return the pipeline running each of ``svc``'s five CRUD roles.

        Each role is one of its operations.

        The pipeline's contract declares the roles (it subclasses ``CrudService``).

        Args:
            svc: The service the pipeline wraps.

        Returns:
            The pipeline running each of ``svc``'s five roles as one.

        """
        return _CrudPipeline(svc, self._subject)


class _CrudPipeline[T, P, ID](CrudOps[T, P, ID], CrudService[T, P, ID]):
    """A CRUD service's roles run through the pipeline tier."""

    def __init__(self, svc: CrudService[T, P, ID], op: Subject) -> None:
        """Bind the service and the pipeline's subject.

        Args:
            svc: The wrapped service.
            op: The pipeline's subject.

        """
        super().__init__(svc, op)
