package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config 保存服务运行所需的全部配置, 来自环境变量(带默认值)。
type Config struct {
	Port       string
	PGHost     string
	PGPort     string
	PGUser     string
	PGPassword string
	PGDB       string
	QdrantURL  string

	// 认证(M7-1): 无状态 JWT(HS256)。JWTSecret 为空且未显式关闭鉴权时, main 会拒绝启动 ——
	// "忘了配密钥"绝不能悄悄退化成"没有鉴权"。
	JWTSecret     string
	TokenTTLHours int
	// AuthDisabled 显式关闭鉴权(仅本地/演示)。开启时启动日志会大声警告。
	AuthDisabled bool

	// 向量模型信息(用于实验配置快照; 默认值与 .env.example / worker 侧保持一致)
	EmbeddingProvider  string
	EmbeddingBaseURL   string
	EmbeddingModel     string
	EmbeddingDim       int
	EmbeddingBatchSize int

	// 生成侧默认值(M4): 提交 run 时未显式给出的字段会取这里, 并写进配置快照(D7/D14)
	GenerationProvider        string
	GenerationBaseURL         string
	GenerationModel           string
	GenerationPromptID        string
	GenerationTemperature     float64
	GenerationMaxTokens       int
	GenerationMaxContextChars int

	// 判定侧默认值(M4-2): 与生成侧同样只在提交 run 时未给出字段时生效, 并写进配置快照
	JudgeProvider        string
	JudgeBaseURL         string
	JudgeModel           string
	JudgeClaimsPromptID  string
	JudgeRubricPromptID  string
	JudgeTemperature     float64
	JudgeMaxTokens       int
	JudgeMaxContextChars int
	JudgeEnableRubric    bool
	JudgeMaxClaims       int
}

// Load 从环境变量加载配置, 缺省时用默认值(与 compose.yaml 默认一致)。
func Load() Config {
	return Config{
		Port:               getenv("PORT", "8080"),
		PGHost:             getenv("PG_HOST", "localhost"),
		PGPort:             getenv("PG_PORT", "5432"),
		PGUser:             getenv("PG_USER", "eval"),
		PGPassword:         getenv("PG_PASSWORD", "eval_dev_password"),
		PGDB:               getenv("PG_DB", "eval_platform"),
		QdrantURL:          getenv("QDRANT_URL", "http://localhost:6333"),
		JWTSecret:          getenv("JWT_SECRET", ""),
		TokenTTLHours:      getenvInt("TOKEN_TTL_HOURS", 12),
		AuthDisabled:       getenvBool("AUTH_DISABLED", false),
		EmbeddingProvider:  getenv("EMBEDDING_PROVIDER", "siliconflow"),
		EmbeddingBaseURL:   getenv("EMBEDDING_BASE_URL", "https://api.siliconflow.cn/v1"),
		EmbeddingModel:     getenv("EMBEDDING_MODEL", "BAAI/bge-m3"),
		EmbeddingDim:       getenvInt("EMBEDDING_DIM", 1024),
		EmbeddingBatchSize: getenvInt("EMBEDDING_BATCH_SIZE", 16),

		GenerationProvider:        getenv("GENERATION_PROVIDER", "siliconflow"),
		GenerationBaseURL:         getenv("GENERATION_BASE_URL", "https://api.siliconflow.cn/v1"),
		GenerationModel:           getenv("GENERATION_MODEL", "deepseek-ai/DeepSeek-V3.2"),
		GenerationPromptID:        getenv("GENERATION_PROMPT_ID", "qa_zh_v1"),
		GenerationTemperature:     getenvFloat("GENERATION_TEMPERATURE", 0),
		GenerationMaxTokens:       getenvInt("GENERATION_MAX_TOKENS", 512),
		GenerationMaxContextChars: getenvInt("GENERATION_MAX_CONTEXT_CHARS", 3000),

		JudgeProvider:        getenv("JUDGE_PROVIDER", "siliconflow"),
		JudgeBaseURL:         getenv("JUDGE_BASE_URL", "https://api.siliconflow.cn/v1"),
		JudgeModel:           getenv("JUDGE_MODEL", "deepseek-ai/DeepSeek-V3.2"),
		JudgeClaimsPromptID:  getenv("JUDGE_CLAIMS_PROMPT_ID", "judge_claims_zh_v1"),
		JudgeRubricPromptID:  getenv("JUDGE_RUBRIC_PROMPT_ID", "judge_rubric_zh_v1"),
		JudgeTemperature:     getenvFloat("JUDGE_TEMPERATURE", 0),
		JudgeMaxTokens:       getenvInt("JUDGE_MAX_TOKENS", 1024),
		JudgeMaxContextChars: getenvInt("JUDGE_MAX_CONTEXT_CHARS", 3000),
		JudgeEnableRubric:    getenvBool("JUDGE_ENABLE_RUBRIC", true),
		JudgeMaxClaims:       getenvInt("JUDGE_MAX_CLAIMS", 12),
	}
}

// PostgresDSN 返回 pgx 标准库 driver 的连接串。
func (c Config) PostgresDSN() string {
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		c.PGUser, c.PGPassword, c.PGHost, c.PGPort, c.PGDB)
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvBool(key string, def bool) bool {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return def
	}
	return value
}

func getenvFloat(key string, def float64) float64 {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return def
	}
	return value
}

func getenvInt(key string, def int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return value
}
