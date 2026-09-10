"""live 测试: 真 PG 上的 run 落库生命周期(RUN_LIVE=1)。自建自清, 不留测试数据。"""

from __future__ import annotations

import os

import pytest

from app.config import Settings
from app.eval.snapshot import build_config_snapshot, git_sha, snapshot_hash
from app.retrieval.chunker import ChunkingConfig
from app.store import (
    CaseResultRow,
    create_run,
    delete_run,
    get_dataset_project,
    get_run,
    list_case_results,
    list_cases,
    save_case_results,
    set_run_status,
)

pytestmark = pytest.mark.skipif(
    os.getenv("RUN_LIVE") != "1", reason="设置 RUN_LIVE=1 才连真实服务"
)

DATASET_ID = int(os.getenv("LIVE_DATASET_ID", "3"))
CORPUS_ID = int(os.getenv("LIVE_CORPUS_ID", "4"))


def test_run_persistence_lifecycle():
    settings = Settings()
    project_id = get_dataset_project(settings.pg_dsn, DATASET_ID)
    assert project_id, "数据集应存在"

    cases = list_cases(settings.pg_dsn, DATASET_ID)
    assert len(cases) >= 2, "评测集至少应有 2 条用例"

    cfg = ChunkingConfig()
    snapshot = build_config_snapshot(
        settings=settings, cfg=cfg, corpus_id=CORPUS_ID, dataset_id=DATASET_ID, top_k=5
    )
    config_hash = snapshot_hash(snapshot)

    run_id = create_run(
        settings.pg_dsn,
        project_id=project_id,
        dataset_id=DATASET_ID,
        corpus_id=CORPUS_ID,
        config_snapshot=snapshot,
        config_hash=config_hash,
        git_sha=git_sha(),
    )
    assert run_id > 0

    try:
        created = get_run(settings.pg_dsn, run_id)
        assert created is not None
        assert created["status"] == "running" and created["started_at"] is not None
        assert created["config_hash"] == config_hash
        assert created["git_sha"]
        # jsonb 往返: 快照存进去还能原样读出来
        assert created["config_snapshot"]["data"]["dataset_id"] == DATASET_ID
        assert created["config_snapshot"]["retrieval"]["top_k"] == 5

        rows = [
            CaseResultRow(
                case_id=cases[0].id,
                retrieved=[{"point_id": "p1", "doc_id": "A01", "score": 0.9}],
                metrics={"recall": 1.0, "hit": 1.0},
                flags=[],
            ),
            CaseResultRow(
                case_id=cases[1].id,
                retrieved=[],
                metrics={"recall": 0.0, "hit": 0.0},
                flags=["retrieval_miss"],
            ),
        ]
        assert save_case_results(settings.pg_dsn, run_id, rows) == 2
        assert save_case_results(settings.pg_dsn, run_id, rows) == 2  # upsert 幂等, 不产生重复行

        saved = list_case_results(settings.pg_dsn, run_id)
        assert len(saved) == 2
        assert {r["case_id"] for r in saved} == {cases[0].id, cases[1].id}
        assert saved[0]["retrieved"][0]["doc_id"] == "A01"
        assert saved[0]["metrics"]["recall"] == 1.0
        assert saved[1]["flags"] == ["retrieval_miss"]

        set_run_status(settings.pg_dsn, run_id, "succeeded", metrics={"recall_at_k": 0.5}, finished=True)
        finished = get_run(settings.pg_dsn, run_id)
        assert finished is not None
        assert finished["status"] == "succeeded"
        assert finished["metrics"]["recall_at_k"] == 0.5
        assert finished["finished_at"] is not None
    finally:
        delete_run(settings.pg_dsn, run_id)

    assert get_run(settings.pg_dsn, run_id) is None
    assert list_case_results(settings.pg_dsn, run_id) == []


def test_failed_run_keeps_error_message():
    settings = Settings()
    project_id = get_dataset_project(settings.pg_dsn, DATASET_ID)
    assert project_id

    snapshot = build_config_snapshot(
        settings=settings,
        cfg=ChunkingConfig(),
        corpus_id=CORPUS_ID,
        dataset_id=DATASET_ID,
        top_k=5,
    )
    run_id = create_run(
        settings.pg_dsn,
        project_id=project_id,
        dataset_id=DATASET_ID,
        corpus_id=CORPUS_ID,
        config_snapshot=snapshot,
        config_hash=snapshot_hash(snapshot),
        git_sha=git_sha(),
    )
    try:
        set_run_status(settings.pg_dsn, run_id, "failed", error="模拟失败: 索引缺失", finished=True)
        run = get_run(settings.pg_dsn, run_id)
        assert run is not None
        assert run["status"] == "failed"
        assert "索引缺失" in str(run["error"])
        assert run["finished_at"] is not None
    finally:
        delete_run(settings.pg_dsn, run_id)