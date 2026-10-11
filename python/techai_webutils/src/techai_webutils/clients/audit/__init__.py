"""AI audit records: a config-selected ``AuditSink`` (``stub`` built in; durable sinks injected)."""

from techai_webutils.clients.audit.builder import (
    AuditSinkConfig,
    AuditSinkKind,
    new_audit_sink_from_config,
)

__all__ = ["AuditSinkConfig", "AuditSinkKind", "new_audit_sink_from_config"]
