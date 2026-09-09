from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    """worker 运行配置。

    环境变量(同名大写覆盖): PG_DSN / QDRANT_URL / LOG_LEVEL
    可读取项目根 .env; 测试用 _env_file=None 屏蔽文件影响。
    """

    model_config = SettingsConfigDict(env_file=".env", extra="ignore")

    pg_dsn: str = "postgresql://eval:eval_dev_password@localhost:5432/eval_platform"
    qdrant_url: str = "http://localhost:6333"
    log_level: str = "INFO"