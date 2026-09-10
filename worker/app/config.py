from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    """worker 运行配置。

    环境变量(同名大写覆盖): PG_DSN / QDRANT_URL / LOG_LEVEL /
    SILICONFLOW_API_KEY / EMBEDDING_BASE_URL / EMBEDDING_MODEL / EMBEDDING_DIM /
    EMBEDDING_BATCH_SIZE / DEEPSEEK_API_KEY / DEEPSEEK_BASE_URL

    读取 .env 的顺序: 项目根 ../.env 再 worker/.env(后者优先) —— 这样无论从
    仓库根还是 worker/ 目录运行 CLI 都能读到同一个 .env。测试用 _env_file=None 屏蔽。
    """

    model_config = SettingsConfigDict(env_file=("../.env", ".env"), extra="ignore")

    # 基础设施
    pg_dsn: str = "postgresql://eval:eval_dev_password@localhost:5432/eval_platform"
    qdrant_url: str = "http://localhost:6333"
    log_level: str = "INFO"

    # embedding (M2: SiliconFlow BAAI/bge-m3, 维度 1024)
    siliconflow_api_key: str = ""
    embedding_base_url: str = "https://api.siliconflow.cn/v1"
    embedding_model: str = "BAAI/bge-m3"
    embedding_dim: int = 1024
    embedding_batch_size: int = 16

    # 生成 / judge (M4: DeepSeek)
    deepseek_api_key: str = ""
    deepseek_base_url: str = "https://api.deepseek.com"