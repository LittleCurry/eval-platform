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

	// 向量模型信息(用于实验配置快照; 默认值与 .env.example / worker 侧保持一致)
	EmbeddingProvider  string
	EmbeddingBaseURL   string
	EmbeddingModel     string
	EmbeddingDim       int
	EmbeddingBatchSize int
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
		EmbeddingProvider:  getenv("EMBEDDING_PROVIDER", "siliconflow"),
		EmbeddingBaseURL:   getenv("EMBEDDING_BASE_URL", "https://api.siliconflow.cn/v1"),
		EmbeddingModel:     getenv("EMBEDDING_MODEL", "BAAI/bge-m3"),
		EmbeddingDim:       getenvInt("EMBEDDING_DIM", 1024),
		EmbeddingBatchSize: getenvInt("EMBEDDING_BATCH_SIZE", 16),
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
