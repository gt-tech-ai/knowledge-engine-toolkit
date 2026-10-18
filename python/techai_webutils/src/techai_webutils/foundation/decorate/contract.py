"""A component's operations, read from its contract (mirrors Go ``decorate/contract.go``).

A base is built from its contract (a Protocol or an abstract class): each public method is one
operation, decorated as ``<component>.<method>``, so the config can tune one operation apart from
the rest of its component.
"""

from __future__ import annotations

import inspect

from techai_webutils.core.errors import AppRuntimeError


def ops_of(contract: type) -> list[str]:
    """Return the operations of ``contract``: its public methods' names, sorted.

    A contract with no public method raises ``AppRuntimeError``: every call on its base would
    fail.
    """
    ops = sorted(
        name
        for name, member in inspect.getmembers(contract)
        if not name.startswith("_")
        and (inspect.isfunction(member) or inspect.ismethoddescriptor(member))
        and not isinstance(inspect.getattr_static(contract, name, None), property)
    )
    if not ops:
        msg = f"decorate: contract {contract.__qualname__} has no methods"
        raise AppRuntimeError(msg)
    return ops
