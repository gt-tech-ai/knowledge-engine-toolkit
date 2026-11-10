"""Walk nested JSON-shaped test data without casting at every step.

Request bodies, decoded payloads and loaded config are ``object`` to the type checker
once they leave a mock's ``call_args`` or ``json.loads``. ``dig`` narrows each step at
run time, so a test reads ``dig(body, "query", "bool")`` instead of a cast per level.
"""

from collections.abc import Mapping, Sequence
from typing import cast


def dig(value: object, *path: str | int) -> object:
    """Return the value at ``path`` inside nested mappings and sequences.

    Args:
        value: The root mapping or sequence.
        *path: Mapping keys and sequence indexes, outermost first.

    Returns:
        The value at the end of ``path``; ``value`` itself when ``path`` is empty.

    Raises:
        TypeError: When a step is neither a mapping nor a sequence indexed by an int.

    """
    for key in path:
        if isinstance(value, Mapping):
            value = cast("Mapping[object, object]", value)[key]
        elif isinstance(value, Sequence) and isinstance(key, int):
            value = value[key]
        else:
            message = f"cannot index {type(value).__name__} with {key!r}"
            raise TypeError(message)
    return value


def items(value: object) -> list[object]:
    """Return ``value`` as a list, for iterating or ``in`` checks on decoded data.

    Args:
        value: A decoded sequence (a list or tuple).

    Returns:
        The elements, in order.

    Raises:
        TypeError: When ``value`` is not a list or tuple.

    """
    if not isinstance(value, list | tuple):
        message = f"expected a list or tuple, got {type(value).__name__}"
        raise TypeError(message)
    return list(cast("Sequence[object]", value))
