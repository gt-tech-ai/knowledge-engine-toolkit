"""Pipelines layer - stateless data transformation.

Pipelines model sequential operations where each stage consumes the output of the prior stage.
Unlike workflows, pipelines do not coordinate state or orchestrate multiple services. They are
pure transformations: Input → Processing → Output. ``PipelineBase.stage`` builds a decorated
``Stage`` (the pipeline tier, ``tier``); ``Stage.then`` chains stages.

Pipelines implements the sixth tier (ARCHITECTURE.md#layers-and-import-direction):
core -> foundation -> clients -> repos -> services -> pipelines -> workflows -> controllers
"""

from techai_webutils.pipelines.pipeline import PipelineBase, Stage, tier

__all__ = ["PipelineBase", "Stage", "tier"]
