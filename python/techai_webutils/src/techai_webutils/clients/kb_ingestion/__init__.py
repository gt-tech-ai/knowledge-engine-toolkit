"""Knowledge-base ingestor clients (Go Layer-2 adapters behind ``core.interfaces.kb_ingestion``).

The Bedrock ingestion-job client (stage/prod) + a no-op stub (dev), the ``PollingIngestor`` /
``RetryingIngestor`` decorators, and the env-aware ``new_kb_ingestor_from_config`` factory. Apps select
an ingestor via config and depend only on the interface.
"""
