from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    """worker 运行配置。

    环境变量(同名大写覆盖): PG_DSN / QDRANT_URL / LOG_LEVEL /
    SILICONFLOW_API_KEY / EMBEDDING_BASE_URL / EMBEDDING_MODEL / EMBEDDING_DIM /
    EMBEDDING_BATCH_SIZE / DEEPSEEK_API_KEY / DEEPSEEK_BASE_URL /
    GENERATION_PROVIDER / GENERATION_BASE_URL / GENERATION_MODEL / GENERATION_API_KEY /
    GENERATION_TEMPERATURE / GENERATION_MAX_TOKENS / GENERATION_MAX_CONTEXT_CHARS /
    GENERATION_TIMEOUT_SECONDS / GENERATION_MAX_RETRIES

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

    # 生成 / judge 的模型端点 (M4)
    # 默认走 SiliconFlow 的 DeepSeek(与 embedding 共用同一个 key, 零新凭证);
    # 若要切官方 DeepSeek: GENERATION_BASE_URL=https://api.deepseek.com + GENERATION_MODEL=deepseek-chat
    # 并填 DEEPSEEK_API_KEY —— 代码路径完全相同, 差别只在配置(D11 的多厂商可切换)。
    deepseek_api_key: str = ""
    deepseek_base_url: str = "https://api.deepseek.com"

    generation_provider: str = "siliconflow"
    generation_base_url: str = "https://api.siliconflow.cn/v1"
    generation_model: str = "deepseek-ai/DeepSeek-V3.2"
    generation_api_key: str = ""  # 为空时按 resolved_generation_api_key 的顺序回落
    generation_temperature: float = 0.0
    generation_max_tokens: int = 512
    # 上下文按**字符**截断(不引 tokenizer 依赖), 实际用量写进 case_results.generation
    generation_max_context_chars: int = 3000
    generation_timeout_seconds: float = 60.0
    generation_max_retries: int = 2

    @property
    def resolved_generation_api_key(self) -> str:
        """生成侧 key 的回落顺序: 专用 key → SiliconFlow → DeepSeek 官方。

        这样"用 SiliconFlow 调 DeepSeek"与"用官方 DeepSeek"两条路只差 base_url/model 两行配置。
        """
        return self.generation_api_key or self.siliconflow_api_key or self.deepseek_api_key