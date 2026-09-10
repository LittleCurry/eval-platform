"""Qdrant collection 管理的 live 测试: 默认 skip, RUN_LIVE=1 时连真容器。"""

from __future__ import annotations

import os

import pytest

from app.config import Settings
from app.retrieval.collections import (
    collection_exists,
    collection_info,
    collection_name,
    delete_collection,
    ensure_collection,
    make_client,
)

pytestmark = pytest.mark.skipif(
    os.getenv("RUN_LIVE") != "1", reason="设置 RUN_LIVE=1 才连真实容器"
)


def test_collection_lifecycle():
    settings = Settings(_env_file=None)
    client = make_client(settings.qdrant_url)
    name = collection_name(corpus_id=9999, cfg_hash="deadbeef" * 8)

    if collection_exists(client, name):
        delete_collection(client, name)

    ensure_collection(client, name, dim=8)
    assert collection_exists(client, name)
    info = collection_info(client, name)
    assert info["dim"] == 8

    # 幂等: 再次 ensure 不报错
    ensure_collection(client, name, dim=8)

    # 维度不一致必须显式报错, 防止把两种 embedding 混进同一 collection
    with pytest.raises(ValueError):
        ensure_collection(client, name, dim=16)

    # recreate=True 时允许重建
    ensure_collection(client, name, dim=16, recreate=True)
    assert collection_info(client, name)["dim"] == 16

    delete_collection(client, name)
    assert not collection_exists(client, name)