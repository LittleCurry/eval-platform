package store

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Qdrant 封装对 Qdrant REST 服务的连通性检查。
type Qdrant struct {
	baseURL string
	client  *http.Client
}

// NewQdrant 创建 Qdrant 客户端。
func NewQdrant(baseURL string) *Qdrant {
	return &Qdrant{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 2 * time.Second},
	}
}

// Ping 请求 /healthz, 状态码 200 视为可用。
func (q *Qdrant) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, q.baseURL+"/healthz", nil)
	if err != nil {
		return fmt.Errorf("build qdrant health request: %w", err)
	}
	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("ping qdrant: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) // 读完 body 以便复用连接
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ping qdrant: unexpected status %d", resp.StatusCode)
	}
	return nil
}
