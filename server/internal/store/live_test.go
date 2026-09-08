package store

import (
	"context"
	"os"
	"testing"
)

// 本地自验真实容器连通性: RUN_LIVE=1 go test ./internal/store/ -run TestLive -v
func TestLivePostgres(t *testing.T) {
	if os.Getenv("RUN_LIVE") == "" {
		t.Skip("设置 RUN_LIVE=1 才连真实容器")
	}
	pg, err := NewPostgres("postgres://eval:eval_dev_password@localhost:5432/eval_platform?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if err := pg.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLiveQdrant(t *testing.T) {
	if os.Getenv("RUN_LIVE") == "" {
		t.Skip("设置 RUN_LIVE=1 才连真实容器")
	}
	q := NewQdrant("http://localhost:6333")
	if err := q.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}
