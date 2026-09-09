package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"eval-platform/server/internal/config"
	serverhttp "eval-platform/server/internal/http"
	"eval-platform/server/internal/store"
)

func main() {
	cfg := config.Load()

	pg, err := store.NewPostgres(cfg.PostgresDSN())
	if err != nil {
		log.Fatalf("初始化 postgres 失败: %v", err)
	}
	defer pg.Close()

	qd := store.NewQdrant(cfg.QdrantURL)

	router := serverhttp.NewRouter(serverhttp.Deps{
		Postgres:  pg,
		Qdrant:    qd,
		Projects:  pg,
		Corpora:   pg,
		Documents: pg,
		Datasets:  pg,
		Cases:     pg,
	})

	srv := &http.Server{Addr: ":" + cfg.Port, Handler: router}

	// 优雅退出: SIGINT/SIGTERM 后最多等 5 秒
	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		<-quit
		log.Println("收到退出信号, 正在关闭...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	log.Printf("eval-platform api 监听 :%s", cfg.Port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server exit: %v", err)
	}
}
