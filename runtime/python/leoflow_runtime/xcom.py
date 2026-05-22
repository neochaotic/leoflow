"""Access XCom inputs injected into the task container by the agent."""

from __future__ import annotations

import json
import os
from typing import Any

XCOM_ENV_PREFIX = "LEOFLOW_XCOM_"


def xcom_pull(name: str, default: Any = None) -> Any:
    """Return the upstream XCom mapped to ``name``, or ``default`` if absent.

    The agent injects each declared input as ``LEOFLOW_XCOM_<NAME>=<json>``;
    the name is matched case-insensitively.
    """
    raw = os.environ.get(XCOM_ENV_PREFIX + name.upper())
    if raw is None:
        return default
    return json.loads(raw)
