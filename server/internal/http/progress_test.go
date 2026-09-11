package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"eval-platform/server/internal/store"
)

func jobWith(status string, heartbeat *time.Time) *store.Job {
	return &store.Job{
		ID:          85,
		RunID:       96,
		Status:      status,
		Progress:    map[string]any{"pending": 28, "running": 1, "succeeded": 1, "failed": 0, "total": 30},
		HeartbeatAt: heartbeat,
	}
}

func TestProgressRunningWithFreshHeartbeat(t *testing.T) {
	recent := time.Now().Add(-5 * time.Second)
	stub := &stubRunStore{
		run: store.Run{ID: 96, Status: "running"},
		job: jobWith("running", &recent),
	}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodGet, "/api/v1/runs/96/progress", "")

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if body["run_id"] != float64(96) || body["status"] != "running" {
		t.Fatalf("顶层字段不符: %v", body)
	}
	if body["stale"] != false {
		t.Fatalf("刚心跳过不应判定为掉线: %v", body["stale"])
	}
	job, ok := body["job"].(map[string]any)
	if !ok {
		t.Fatalf("job 字段缺失: %v", body["job"])
	}
	progress, ok := job["progress"].(map[string]any)
	if !ok || progress["succeeded"] != float64(1) || progress["total"] != float64(30) {
		t.Fatalf("progress 不符: %v", job["progress"])
	}
	if stub.gotJobRunID != 96 {
		t.Fatalf("应按 run_id 查任务, 实际 %d", stub.gotJobRunID)
	}
}

func TestProgressDetectsStaleWorker(t *testing.T) {
	old := time.Now().Add(-10 * time.Minute)
	stub := &stubRunStore{
		run: store.Run{ID: 96, Status: "running"},
		job: jobWith("running", &old),
	}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodGet, "/api/v1/runs/96/progress", "")

	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["stale"] != true {
		t.Fatalf("心跳过期应判定掉线: %v", body)
	}
}

func TestProgressFinishedRunIsNotStale(t *testing.T) {
	old := time.Now().Add(-10 * time.Minute)
	stub := &stubRunStore{
		run: store.Run{ID: 96, Status: "succeeded"},
		job: jobWith("succeeded", &old),
	}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodGet, "/api/v1/runs/96/progress", "")

	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["stale"] != false {
		t.Fatalf("已完成任务不应判定掉线: %v", body)
	}
}

func TestProgressWithoutJobReturnsNull(t *testing.T) {
	// M2 的 CLI 直跑记录没有 job
	stub := &stubRunStore{run: store.Run{ID: 3, Status: "succeeded"}, job: nil}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodGet, "/api/v1/runs/3/progress", "")

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["job"] != nil {
		t.Fatalf("没有队列任务时应返回 job=null: %v", body["job"])
	}
}

func TestProgressRunNotFound(t *testing.T) {
	stub := &stubRunStore{getErr: store.ErrNotFound}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodGet, "/api/v1/runs/999/progress", "")

	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w.Code)
	}
}

func TestProgressJobQueryError(t *testing.T) {
	stub := &stubRunStore{
		run:    store.Run{ID: 96, Status: "running"},
		jobErr: errors.New("boom"),
	}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodGet, "/api/v1/runs/96/progress", "")

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", w.Code)
	}
}

func TestProgressBadID(t *testing.T) {
	r := runRouter(t, &stubRunStore{})

	w := doJSON(t, r, http.MethodGet, "/api/v1/runs/abc/progress", "")

	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", w.Code)
	}
}
