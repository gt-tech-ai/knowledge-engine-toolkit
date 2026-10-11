"""LLM provider clients (Go Layer-2 adapters behind ``core.interfaces.llm.LLMProvider``).

The raw providers — Bedrock (stage/prod) + a deterministic stub (dev) — plus the env-aware
``new_llm_from_config`` factory. Apps select a provider via config and depend only on the interface.
"""
