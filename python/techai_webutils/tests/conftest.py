"""Shared configuration for the techai_webutils test suites.

Per-directory ``conftest.py`` files add fixtures scoped to their own test module
(e.g., ``clients/conftest.py``, ``foundation/conftest.py``). Test doubles are built
inline with ``unittest.mock`` (``MagicMock`` / ``AsyncMock``, ``spec``-bound to the
real ``core`` interface) in each test — there is no shared fakes package.
"""
