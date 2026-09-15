package store

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func qdrantResponse(t *testing.T, result string) []byte {
	t.Helper()
	return []byte(`{"result":` + result + `,"status":"ok","time":0.001}`)
}

func TestRetrievePointsParsesStringAndNumericIDs(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("请求体不是 JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(qdrantResponse(t, `[
			{"id":"4c51cfa4-ca7c-59b6-b281-63139460e5e8","payload":{"doc_id":"B02","text":"超过 7 天无跟进会自动回收","section":"自动回收"}},
			{"id":77,"payload":{"doc_id":"A01","text":"数字 id 也要能对上"}}
		]`))
	}))
	defer server.Close()

	q := NewQdrant(server.URL)
	points, err := q.RetrievePoints(context.Background(), "corpus4_5f45e034",
		[]string{"4c51cfa4-ca7c-59b6-b281-63139460e5e8", "77"})
	if err != nil {
		t.Fatalf("RetrievePoints 失败: %v", err)
	}

	if gotPath != "/collections/corpus4_5f45e034/points" {
		t.Fatalf("路径不符: %s", gotPath)
	}
	if gotBody["with_payload"] != true || gotBody["with_vector"] != false {
		t.Fatalf("必须带 payload 且不要向量: %v", gotBody)
	}
	if ids, ok := gotBody["ids"].([]any); !ok || len(ids) != 2 {
		t.Fatalf("ids 未透传: %v", gotBody["ids"])
	}

	if len(points) != 2 {
		t.Fatalf("应返回两条, 实际 %d", len(points))
	}
	first := points["4c51cfa4-ca7c-59b6-b281-63139460e5e8"]
	if first.DocID != "B02" || !strings.Contains(first.Text, "自动回收") || first.Section != "自动回收" {
		t.Fatalf("payload 解析不符: %+v", first)
	}
	if points["77"].Text != "数字 id 也要能对上" {
		t.Fatalf("数字 id 未归一成字符串: %+v", points)
	}
}

func TestRetrievePointsMissingIDsAreSimplyAbsent(t *testing.T) {
	// 集合被重建/切分配置变更时, 请求的 id 可能一个都不在 -> 不能当错误, 由调用方降级
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(qdrantResponse(t, `[]`))
	}))
	defer server.Close()

	points, err := NewQdrant(server.URL).RetrievePoints(context.Background(), "c", []string{"a", "b"})
	if err != nil {
		t.Fatalf("空结果不应报错: %v", err)
	}
	if len(points) != 0 {
		t.Fatalf("应为空 map, 实际 %v", points)
	}
}

func TestRetrievePointsEmptyIDsSkipsRequest(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Write(qdrantResponse(t, `[]`))
	}))
	defer server.Close()

	points, err := NewQdrant(server.URL).RetrievePoints(context.Background(), "c", nil)
	if err != nil || len(points) != 0 {
		t.Fatalf("空 ids 应直接返回空 map: %v %v", points, err)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("空 ids 不该发起请求")
	}
}

func TestRetrievePointsNonOKStatusIsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"status":{"error":"Collection not found"}}`))
	}))
	defer server.Close()

	_, err := NewQdrant(server.URL).RetrievePoints(context.Background(), "missing", []string{"a"})
	if err == nil {
		t.Fatal("404 必须报错, 否则前端会把「集合不存在」当成「没有正文」")
	}
	if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "Collection not found") {
		t.Fatalf("错误信息应带状态码与原因: %v", err)
	}
}

func TestRetrievePointsBadBodyIsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`not json`))
	}))
	defer server.Close()

	if _, err := NewQdrant(server.URL).RetrievePoints(context.Background(), "c", []string{"a"}); err == nil {
		t.Fatal("响应不是 JSON 时必须报错")
	}
}

func TestParseQdrantPointsSkipsEmptyIDs(t *testing.T) {
	points, err := parseQdrantPoints([]byte(`{"result":[{"id":null,"payload":{"text":"x"}},{"id":"ok","payload":{"text":"y"}}]}`))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(points) != 1 || points["ok"].Text != "y" {
		t.Fatalf("空 id 应被跳过: %v", points)
	}
}
