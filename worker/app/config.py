from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    """worker 运行配置。

    环境变量(同名大写覆盖): PG_DSN / QDRANT_URL / LOG_LEVEL /
    SILICONFLOW_API_KEY / EMBEDDING_BASE_URL / EMBEDDING_MODEL / EMBEDDING_DIM /
    EMBEDDING_BATCH_SIZE / DEEPSEEK_API_KEY / DEEPSEEK_BASE_URL /
    GENERATION_PROVIDER / GENERATION_BASE_URL / GENERATION_MODEL / GENERATION_API_KEY /
    GENERATION_TEMPERATURE / GENERATION_MAX_TOKENS / GENERATION_MAX_CONTEXT_CHARS /
    GENERATION_TIMEOUT_SECONDS / GENERATION_MAX_RETRIES /
    JUDGE_PROVIDER / JUDGE_BASE_URL / JUDGE_MODEL / JUDGE_API_KEY /
    JUDGE_TEMPERATURE / JUDGE_MAX_TOKENS / JUDGE_MAX_CONTEXT_CHARS /
    JUDGE_CLAIMS_PROMPT_ID / JUDGE_RUBRIC_PROMPT_ID / JUDGE_ENABLE_RUBRIC /
    JUDGE_MAX_CLAIMS / JUDGE_MAX_RETRIES / JUDGE_USE_CACHE / JUDGE_TIMEOUT_SECONDS

    读取 .env 的顺序: 项目根 ../.env 再 worker/.env(后者优先) —— 这样无论从
    仓库根还是 worker/ 目录运行 CLI 都能读到同一个 .env。测试用 _env_file=None 屏蔽。

    注意(D14): 生成与 judge 的**实验参数**以 run 的配置快照为准, 这里的值只作为
    提交时未提供字段的默认值 / 快照缺字段时的兜底, 以及提供 api_key。
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

    # judge (M4-2): 答案的 claim 级事实核查 + rubric 打分
    judge_provider: str = "siliconflow"
    judge_base_url: str = "https://api.siliconflow.cn/v1"
    judge_model: str = "deepseek-ai/DeepSeek-V3.2"
    judge_api_key: str = ""  # 为空时按 resolved_judge_api_key 的顺序回落
    judge_temperature: float = 0.0
    # judge 输出比生成长(claims 列表 + evidence), 默认给足
    judge_max_tokens: int = 1024
    judge_max_context_chars: int = 3000
    judge_claims_prompt_id: str = "judge_claims_zh_v1"
    judge_rubric_prompt_id: str = "judge_rubric_zh_v2"
    judge_enable_rubric: bool = True
    # claims 上限: 成本护栏。超出即截断并记录 truncated_claims
    judge_max_claims: int = 12
    # 协议失败(输出不是合法 JSON)时的重试次数; 用尽则让该 case 失败(不写猜的判定)
    judge_max_retries: int = 2
    judge_use_cache: bool = True
    judge_timeout_seconds: float = 60.0

    @property
    def resolved_generation_api_key(self) -> str:
        """生成侧 key 的回落顺序: 专用 key → SiliconFlow → DeepSeek 官方。

        这样"用 SiliconFlow 调 DeepSeek"与"用官方 DeepSeek"两条路只差 base_url/model 两行配置。
        """
        return self.generation_api_key or self.siliconflow_api_key or self.deepseek_api_key

    @property
    def resolved_judge_api_key(self) -> str:
        """judge 侧 key 的回落顺序: 专用 key → SiliconFlow → DeepSeek 官方(与生成侧同规则)。"""
        return self.judge_api_key or self.siliconflow_api_key or self.deepseek_api_key