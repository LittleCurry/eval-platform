"""CLI: 评测 worker(队列消费者)。

用法:
    python -m app.cli.worker                       # 常驻, 轮询领任务
    python -m app.cli.worker --once                # 只领一个任务并跑完(验证用)
    python -m app.cli.worker --once --job-id 19    # 只跑指定任务(重跑/调试)
    python -m app.cli.worker --once --max-items 5  # 只跑 5 条, 其余放回队列(验证续跑)

输出: 每个任务的 JSON 摘要(含 processed/succeeded/failed/elapsed_ms)。
"""
from __future__ import annotations

import argparse
import json
import signal
import sys

from app.config import Settings
from app.eval.queue_runner import QueueRunner, RunnerOptions
from app.logging_conf import setup_logging


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="评测 worker(队列消费者)")
    parser.add_argument("--once", action="store_true", help="只执行一个任务后退出")
    parser.add_argument("--job-id", type=int, default=0, help="只执行指定任务(重跑/调试用)")
    parser.add_argument("--max-items", type=int, default=0, help="本次最多处理多少条用例(0=不限)")
    parser.add_argument("--batch-size", type=int, default=8, help="每批领取的用例数")
    parser.add_argument("--max-retries", type=int, default=3, help="单条用例最大重试次数")
    parser.add_argument("--poll-interval", type=float, default=1.0, help="空闲轮询间隔(秒)")
    parser.add_argument("--heartbeat-seconds", type=float, default=10.0, help="心跳刷新间隔(秒)")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    settings = Settings()
    setup_logging(settings.log_level)

    options = RunnerOptions(
        batch_size=args.batch_size,
        max_retries=args.max_retries,
        heartbeat_seconds=args.heartbeat_seconds,
        poll_interval=args.poll_interval,
        max_items=args.max_items or None,
    )
    runner = QueueRunner(settings, options)

    def _handle_signal(signum: int, _frame: object) -> None:
        runner.log.info("signal_received", signal=signum)
        runner.request_stop()

    signal.signal(signal.SIGINT, _handle_signal)
    signal.signal(signal.SIGTERM, _handle_signal)

    if args.once:
        summary = runner.run_once(args.job_id or None)
        if summary is None:
            print(json.dumps({"status": "idle", "message": "没有待执行任务"}, ensure_ascii=False))
            return 0
        print(json.dumps(summary.to_json(), ensure_ascii=False, indent=2))
        return 0

    runner.run_forever()
    return 0


if __name__ == "__main__":
    sys.exit(main())