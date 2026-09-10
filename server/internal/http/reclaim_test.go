package http

import (
	"encoding/json"
	"net/http"
	"testing"

	"eval-platform/server/internal/store"
)

func TestReclaimDefaultsToSixtySeconds(t *testing.T) {
	stub := &stubRunStore{reclaimResult: store.ReclaimResult{Jobs: 1, Items: 3}}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodPost, "/api/v1/jobs/reclaim", "")

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if stub.gotOlderThan != 60 {
		t.Fatalf("默认阈值应为 60 秒, 实际 %v", stub.gotOlderThan)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["jobs"] != float64(1) || body["items"] != float64(3) {
		t.Fatalf("响应不符: %v", body)
	}
}

func TestReclaimHonoursOlderThanParam(t *testing.T) {
	stub := &stubRunStore{reclaimResult: store.ReclaimResult{Jobs: 2, Items: 7}}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodPost, "/api/v1/jobs/reclaim?older_than_seconds=0", "")

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	if stub.gotOlderThan != 0 {
		t.Fatalf("阈值未透传: %v", stub.gotOlderThan)
	}
}

func TestReclaimRejectsNegativeThreshold(t *testing.T) {
	stub := &stubRunStore{}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodPost, "/api/v1/jobs/reclaim?older_than_seconds=-5", "")

	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", w.Code)
	}
	if stub.reclaimCallCount != 0 {
		t.Fatal("非法参数不应触发接管")
	}
}

func TestReclaimNoStaleJobsReturnsZero(t *testing.T) {
	stub := &stubRunStore{}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodPost, "/api/v1/jobs/reclaim", "")

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["jobs"] != float64(0) || body["items"] != float64(0) {
		t.Fatalf("无僵尸任务时应返回 0: %v", body)
	}
}
