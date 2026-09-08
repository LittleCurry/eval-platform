package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // 注册名为 "pgx" 的 database/sql driver
)

// Postgres 封装到 PostgreSQL 的连接, 提供连通性检查。
type Postgres struct {
	db *sql.DB
}

// NewPostgres 打开连接池(不立即建连; 连接异常统一由 Ping 暴露)。
func NewPostgres(dsn string) (*Postgres, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	return &Postgres{db: db}, nil
}

// Ping 带超时检查数据库连通性。
func (p *Postgres) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := p.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping postgres: %w", err)
	}
	return nil
}

// Close 释放连接池。
func (p *Postgres) Close() error {
	return p.db.Close()
}
