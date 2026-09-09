package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(_ context.Context) error { return f.err }

func newTestRouter(pg, qd Pinger, projects ProjectStore, corpora CorpusStore, docs DocumentStore) *gin.Engine {
	gin.SetMode(gin.TestMode)
	return NewRouter(Deps{
		Postgres:  pg,
		Qdrant:    qd,
		Projects:  projects,
		Corpora:   corpora,
		Documents: docs,
	})
}

func doHealthz(t *testing.T, pg, qd Pinger) (int, map[string]any) {
	t.Helper()
	r := newTestRouter(pg, qd, nil, nil, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.ServeHTTP(w, req)

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (body=%s)", err, w.Body.String())
	}
	return w.Code, body
}

func TestHealthzAllUp(t *testing.T) {
	code, body := doHealthz(t, fakePinger{}, fakePinger{})
	if code != http.StatusOK {
		t.Errorf("code = %d, want 200", code)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %v, want ok", body["status"])
	}
	checks := body["checks"].(map[string]any)
	if checks["postgres"] != "up" || checks["qdrant"] != "up" {
		t.Errorf("checks = %v, want 全 up", checks)
	}
}

func TestHealthzPostgresDown(t *testing.T) {
	code, body := doHealthz(t, fakePinger{err: errNotInitialized}, fakePinger{})
	if code != http.StatusServiceUnavailable {
		t.Errorf("code = %d, want 503", code)
	}
	if body["status"] != "degraded" {
		t.Errorf("status = %v, want degraded", body["status"])
	}
	checks := body["checks"].(map[string]any)
	if checks["postgres"] != "down" || checks["qdrant"] != "up" {
		t.Errorf("checks = %v, want postgres=down qdrant=up", checks)
	}
}
