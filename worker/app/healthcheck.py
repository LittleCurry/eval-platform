"""worker 最小连通性自检。

用法: python -m app.healthcheck
退出码: 0 = 全部依赖可达; 1 = 存在不可达依赖。
"""
from __future__ import annotations

import json
import sys

import httpx
import psycopg
import structlog

from app.config import Settings
from app.logging_conf import setup_logging


def check_postgres(dsn: str) -> tuple[bool, str]:
    try:
        with psycopg.connect(dsn, connect_timeout=3) as conn:
            conn.execute("SELECT 1")
        return True, "up"
    except Exception as exc:  # noqa: BLE001 自检需要吞掉一切异常并报告
        return False, f"down ({exc.__class__.__name__}: {exc})"


def check_qdrant(base_url: str) -> tuple[bool, str]:
    try:
        resp = httpx.get(f"{base_url}/healthz", timeout=3.0, trust_env=False)
        ok = resp.status_code == 200
        return ok, ("up" if ok else f"down (http {resp.status_code})")
    except Exception as exc:  # noqa: BLE001
        return False, f"down ({exc.__class__.__name__}: {exc})"


def main() -> int:
    settings = Settings()
    setup_logging(settings.log_level)
    log = structlog.get_logger("worker.healthcheck")

    checks: dict[str, tuple[bool, str]] = {
        "postgres": check_postgres(settings.pg_dsn),
        "qdrant": check_qdrant(settings.qdrant_url),
    }
    all_up = True
    for name, (ok, detail) in checks.items():
        log.info("check", dep=name, ok=ok, detail=detail)
        all_up = all_up and ok

    payload = {
        "status": "ok" if all_up else "degraded",
        "checks": {name: ("up" if ok else "down") for name, (ok, _) in checks.items()},
    }
    print(json.dumps(payload, ensure_ascii=False))
    return 0 if all_up else 1


if __name__ == "__main__":
    sys.exit(main())