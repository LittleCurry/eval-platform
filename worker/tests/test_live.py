"""连真实容器的 live 测试: 默认 skip, RUN_LIVE=1 时执行。"""

import os

import pytest

from app.config import Settings
from app.healthcheck import check_postgres, check_qdrant

pytestmark = pytest.mark.skipif(
    os.getenv("RUN_LIVE") != "1", reason="设置 RUN_LIVE=1 才连真实容器"
)


def test_live_postgres():
    ok, _ = check_postgres(Settings(_env_file=None).pg_dsn)
    assert ok


def test_live_qdrant():
    ok, _ = check_qdrant(Settings(_env_file=None).qdrant_url)
    assert ok