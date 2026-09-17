package main

import (
	"context"
	"eval-platform/server/internal/eval"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"eval-platform/server/internal/auth"
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

	// 认证(M7-1): fail-fast 而不是"退化成一个免登录的 API"。
	// 忘了配密钥是运维最常见的一类事故, 让它在启动阶段就炸掉, 比跑到线上才发现好。
	var authService *auth.Service
	if cfg.AuthDisabled {
		log.Printf("⚠️  AUTH_DISABLED=1: 鉴权已关闭, /api/v1 全部接口不需登录 —— 仅限本地演示")
	} else {
		if cfg.JWTSecret == "" {
			log.Fatalf("缺少 JWT_SECRET: 生成一个再启动, 例如  openssl rand -base64 48\n" +
				"(只想本地不登录跑: 设 AUTH_DISABLED=1)")
		}
		issuer, err := auth.NewIssuer(cfg.JWTSecret, time.Duration(cfg.TokenTTLHours)*time.Hour)
		if err != nil {
			log.Fatalf("JWT_SECRET 不合法: %v", err)
		}
		authService = auth.NewService(pg, issuer)
		log.Printf("鉴权已启用: HS256, token 有效期 %d 小时", cfg.TokenTTLHours)
	}

	router := serverhttp.NewRouter(serverhttp.Deps{
		Postgres:  pg,
		Qdrant:    qd,
		Projects:  pg,
		Corpora:   pg,
		Documents: pg,
		Datasets:  pg,
		Cases:     pg,
		Runs:      pg,
		// 同一个 Qdrant 客户端兼作"按 id 取 chunk 正文"(M4-4.1): 报告抽屉要用上下文原文
		QdrantPoints: qd,
		Profiles:     pg,
		Annotations:  pg,
		HumanGold:    pg,
		Auth:         authService,
		// 配置模板预览要能算出与提交一致的指纹, 因此与 run 共用同一批默认值(M5-2)
		EvalEmbedding: eval.EmbeddingConfig{
			Provider:  cfg.EmbeddingProvider,
			BaseURL:   cfg.EmbeddingBaseURL,
			Model:     cfg.EmbeddingModel,
			Dim:       cfg.EmbeddingDim,
			BatchSize: cfg.EmbeddingBatchSize,
		},
		EvalGeneration: eval.GenerationConfig{
			Provider:        cfg.GenerationProvider,
			BaseURL:         cfg.GenerationBaseURL,
			Model:           cfg.GenerationModel,
			PromptID:        cfg.GenerationPromptID,
			Temperature:     cfg.GenerationTemperature,
			MaxTokens:       cfg.GenerationMaxTokens,
			MaxContextChars: cfg.GenerationMaxContextChars,
		},
		EvalJudge: eval.JudgeConfig{
			Provider:        cfg.JudgeProvider,
			BaseURL:         cfg.JudgeBaseURL,
			Model:           cfg.JudgeModel,
			ClaimsPromptID:  cfg.JudgeClaimsPromptID,
			RubricPromptID:  cfg.JudgeRubricPromptID,
			Temperature:     cfg.JudgeTemperature,
			MaxTokens:       cfg.JudgeMaxTokens,
			MaxContextChars: cfg.JudgeMaxContextChars,
			EnableRubric:    cfg.JudgeEnableRubric,
			MaxClaims:       cfg.JudgeMaxClaims,
		},
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
