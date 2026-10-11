"""Token cost arithmetic: a versioned per-model price table and an exact ``Decimal`` ``cost_of``.

``cost_usd = (input * input_per_1k + output * output_per_1k + embed * embed_per_1k) / 1000``,
quantized to the micro-dollar with banker's rounding. The price table is configuration data; an
unpriced model fails loudly rather than costing zero.
"""

from __future__ import annotations

from dataclasses import dataclass
from decimal import ROUND_HALF_EVEN, Decimal
from typing import TYPE_CHECKING

from techai_webutils.core.errors.errors import AppError, ErrorCode

if TYPE_CHECKING:
    from collections.abc import Mapping

    from techai_webutils.core.interfaces.token_ledger import UsageRecord

MICRO_DOLLAR = Decimal("0.000001")
"""The quantum every cost is rounded to."""

_PER = Decimal(1000)
"""Prices are per thousand tokens."""


@dataclass(frozen=True, slots=True)
class ModelPrice:
    """A model's price in US dollars per thousand tokens."""

    input_per_1k: Decimal
    """Price per 1k prompt tokens."""
    output_per_1k: Decimal
    """Price per 1k completion tokens."""
    embed_per_1k: Decimal
    """Price per 1k embedding tokens."""


@dataclass(frozen=True, slots=True)
class PriceTable:
    """A versioned set of model prices (configuration data)."""

    prices: Mapping[str, ModelPrice]
    """Price per model id."""
    version: str
    """Identifies this table; recorded on each usage as ``pricing_version``."""


def cost_of(record: UsageRecord, table: PriceTable) -> Decimal:
    """Return ``record``'s cost in US dollars, quantized to ``MICRO_DOLLAR`` (half-even).

    Raises:
        AppError: ``INVALID_INPUT`` when ``record.model`` has no price in ``table``.

    """
    price = table.prices.get(record.model)
    if price is None:
        raise AppError(
            ErrorCode.INVALID_INPUT,
            f"no price for model {record.model!r}",
            details={"model": record.model, "pricing_version": table.version},
        )
    total = (
        record.input_tokens * price.input_per_1k
        + record.output_tokens * price.output_per_1k
        + record.embed_tokens * price.embed_per_1k
    ) / _PER
    return total.quantize(MICRO_DOLLAR, rounding=ROUND_HALF_EVEN)
