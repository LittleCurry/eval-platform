package config

import (
	"fmt"
	"os"
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
}

// Load 从环境变量加载配置, 缺省时用默认值(与 compose.yaml 默认一致)。
func Load() Config {
	return Config{
		Port:       getenv("PORT", "8080"),
		PGHost:     getenv("PG_HOST", "localhost"),
		PGPort:     getenv("PG_PORT", "5432"),
		PGUser:     getenv("PG_USER", "eval"),
		PGPassword: getenv("PG_PASSWORD", "eval_dev_password"),
		PGDB:       getenv("PG_DB", "eval_platform"),
		QdrantURL:  getenv("QDRANT_URL", "http://localhost:6333"),
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
