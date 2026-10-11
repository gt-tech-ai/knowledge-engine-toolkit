"""Clients layer - external system integration abstractions.

This layer contains gRPC client/server factories, decorators, messaging (SQS),
storage (S3), cache decorators, and integration code for external systems.

Clients implements the third tier (ARCHITECTURE.md#layers-and-import-direction):
core -> foundation -> clients -> repos -> services -> pipelines -> workflows -> controllers
"""
