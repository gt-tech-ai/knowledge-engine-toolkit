"""Pipeline interfaces for data transformation.

Defines the abstract contracts for pipeline implementations (sync and async).
Pipelines are stateless sequential transformations.
"""

from __future__ import annotations

from abc import ABC, abstractmethod


class Pipeline[In, Out](ABC):
    """Abstract pipeline for synchronous data transformation.

    Pipelines model stateless sequential operations where each stage consumes
    the output of the prior stage. Unlike workflows, pipelines do not coordinate
    state or orchestrate multiple services.

    Examples: CPU-bound NLP processing, document parsing, ETL transformations.
    """

    @abstractmethod
    def execute(self, input_data: In) -> Out:
        """Transform the input into output.

        Implementations should be stateless and safe for concurrent use.
        """
        ...


class AsyncPipeline[In, Out](ABC):
    """Abstract pipeline for asynchronous data transformation.

    Async variant of Pipeline for I/O-bound transformations or integrations
    with async services.

    Examples: Async API calls for data enrichment, async file I/O, streaming
    document processing.
    """

    @abstractmethod
    async def execute(self, input_data: In) -> Out:
        """Transform the input into output asynchronously.

        Implementations should be stateless and safe for concurrent use.
        """
        ...
