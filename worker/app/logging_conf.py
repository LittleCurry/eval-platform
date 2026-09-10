import logging

import structlog


def setup_logging(level: str = "INFO") -> None:
    """统一日志: 结构化 JSON 输出, 便于后续采集与检索。"""
    logging.basicConfig(level=level.upper(), format="%(message)s")
    logging.getLogger("httpx").setLevel(logging.WARNING)
    structlog.configure(
        processors=[
            structlog.processors.add_log_level,
            structlog.processors.TimeStamper(fmt="iso"),
            structlog.processors.JSONRenderer(ensure_ascii=False),
        ],
        wrapper_class=structlog.make_filtering_bound_logger(logging.getLevelName(level.upper())),
        logger_factory=structlog.PrintLoggerFactory(),
    )