package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Qdrant 封装对 Qdrant REST 服务的访问: 健康检查 + 按 id 取回 chunk 正文。
//
// 为什么报告要问向量库要正文: chunk 正文只在向量库里有一份, `case_results.retrieved`
// 只存 point_id/doc_id/score。把它再落一份进 PG 就成了双写(process.md D17),
// 而集合名由 corpus_id + 切分指纹推导, 所以历史 run 也能取回**当时那份**正文。
type Qdrant struct {
	baseURL string
	client  *http.Client
}

// qdrantPointsTimeout 取正文的单次超时: 比健康检查(2s)宽松, 但不能拖住报告页。
const qdrantPointsTimeout = 5 * time.Second

// QdrantPoint 一个 chunk 的 payload(只取报告要展示的字段)。
type QdrantPoint struct {
	PointID string `json:"point_id"`
	DocID   string `json:"doc_id"`
	Text    string `json:"text"`
	Section string `json:"section"`
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

// RetrievePoints 按 point id 批量取回 payload 正文。
//
// 返回 map[point_id]QdrantPoint: 请求里存在但库里没有的 id 不会出现在结果中 ——
// 调用方据此把该 chunk 标成"正文缺失"(集合被重建/切分配置变更时不至于整页报错)。
func (q *Qdrant) RetrievePoints(
	ctx context.Context, collection string, ids []string,
) (map[string]QdrantPoint, error) {
	out := make(map[string]QdrantPoint, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, qdrantPointsTimeout)
	defer cancel()

	body, err := json.Marshal(map[string]any{
		"ids":          ids,
		"with_payload": true,
		"with_vector":  false,
	})
	if err != nil {
		return nil, fmt.Errorf("encode qdrant retrieve body: %w", err)
	}

	url := fmt.Sprintf("%s/collections/%s/points", q.baseURL, collection)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build qdrant retrieve request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("retrieve qdrant points: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read qdrant retrieve response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"retrieve qdrant points: status %d (%s)", resp.StatusCode, truncateForError(string(raw), 200),
		)
	}
	return parseQdrantPoints(raw)
}

// parseQdrantPoints 解析 {"result":[{"id":..,"payload":{..}}]}。
//
// id 在 Qdrant 里既可以是字符串也可以是数字, 这里统一归一成字符串, 以便与落库的
// `retrieved[].point_id` 对齐(worker 侧写的是 UUID 形态的字符串)。
func parseQdrantPoints(raw []byte) (map[string]QdrantPoint, error) {
	var decoded struct {
		Result []struct {
			ID      json.RawMessage `json:"id"`
			Payload struct {
				DocID   string `json:"doc_id"`
				Text    string `json:"text"`
				Section string `json:"section"`
			} `json:"payload"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("decode qdrant retrieve response: %w", err)
	}

	out := make(map[string]QdrantPoint, len(decoded.Result))
	for _, item := range decoded.Result {
		id := normalizePointID(item.ID)
		if id == "" {
			continue
		}
		out[id] = QdrantPoint{
			PointID: id,
			DocID:   item.Payload.DocID,
			Text:    item.Payload.Text,
			Section: item.Payload.Section,
		}
	}
	return out, nil
}

// normalizePointID 把 Qdrant 的 id 归一成字符串; JSON null / 空值一律视为"无 id"。
func normalizePointID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	text := strings.TrimSpace(string(raw))
	if text == "null" {
		return ""
	}
	if strings.HasPrefix(text, "\"") {
		var value string
		if err := json.Unmarshal(raw, &value); err == nil {
			return value
		}
		return ""
	}
	return text // 数字 id 原样当字符串
}

// truncateForError 截断 body 片段, 避免错误信息里塞进整个响应。
func truncateForError(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
