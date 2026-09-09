package http

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"eval-platform/server/internal/store"
)

// ---- fake stores ----

type stubProjectStore struct {
	createErr error
	created   *store.Project
}

func (s *stubProjectStore) ListProjects(ctx context.Context) ([]store.Project, error) {
	return nil, nil
}

func (s *stubProjectStore) CreateProject(ctx context.Context, name, desc string) (store.Project, error) {
	if s.createErr != nil {
		return store.Project{}, s.createErr
	}
	p := store.Project{ID: 1, Name: name, Description: desc}
	if s.created != nil {
		*s.created = p
	}
	return p, nil
}

type stubCorpusStore struct {
	createErr error
}

func (s *stubCorpusStore) ListCorpora(ctx context.Context, projectID int64) ([]store.Corpus, error) {
	return nil, nil
}

func (s *stubCorpusStore) CreateCorpus(ctx context.Context, projectID int64, name, sourceType string) (store.Corpus, error) {
	if s.createErr != nil {
		return store.Corpus{}, s.createErr
	}
	return store.Corpus{ID: 1, ProjectID: projectID, Name: name, SourceType: sourceType}, nil
}

func (s *stubCorpusStore) GetCorpus(ctx context.Context, id int64) (store.Corpus, error) {
	return store.Corpus{}, store.ErrNotFound
}

func (s *stubCorpusStore) UpdateCorpus(ctx context.Context, id int64, name, sourceType *string) (store.Corpus, error) {
	return store.Corpus{}, nil
}

func (s *stubCorpusStore) DeleteCorpus(ctx context.Context, id int64) error { return nil }

type stubDocumentStore struct {
	createErr error
	inserted  int64
}

func (s *stubDocumentStore) ListDocuments(ctx context.Context, corpusID int64) ([]store.Document, error) {
	return nil, nil
}

func (s *stubDocumentStore) CreateDocuments(ctx context.Context, corpusID int64, docs []store.Document) (int64, error) {
	if s.createErr != nil {
		return 0, s.createErr
	}
	return s.inserted, nil
}

func (s *stubDocumentStore) GetDocument(ctx context.Context, id int64) (store.Document, error) {
	return store.Document{}, store.ErrNotFound
}

func (s *stubDocumentStore) DeleteDocument(ctx context.Context, id int64) error { return nil }

// ---- helpers ----

func doJSON(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func testRouter(t *testing.T, projects ProjectStore, corpora CorpusStore, docs DocumentStore) http.Handler {
	t.Helper()
	return newTestRouter(fakePinger{}, fakePinger{}, projects, corpora, docs)
}

// ---- tests ----

func TestCreateProjectMissingName(t *testing.T) {
	r := testRouter(t, &stubProjectStore{}, nil, nil)
	w := doJSON(t, r, http.MethodPost, "/api/v1/projects", `{"name":"  "}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", w.Code)
	}
}

func TestCreateProjectConflict(t *testing.T) {
	r := testRouter(t, &stubProjectStore{createErr: store.ErrConflict}, nil, nil)
	w := doJSON(t, r, http.MethodPost, "/api/v1/projects", `{"name":"重名"}`)
	if w.Code != http.StatusConflict {
		t.Errorf("code = %d, want 409", w.Code)
	}
}

func TestCreateProjectSuccess(t *testing.T) {
	var created store.Project
	r := testRouter(t, &stubProjectStore{created: &created}, nil, nil)
	w := doJSON(t, r, http.MethodPost, "/api/v1/projects", `{"name":"知简CRM评测","description":"desc"}`)
	if w.Code != http.StatusCreated {
		t.Errorf("code = %d, want 201", w.Code)
	}
	if created.Name != "知简CRM评测" {
		t.Errorf("name = %q, want 知简CRM评测", created.Name)
	}
}

func TestListCorporaRequiresProjectID(t *testing.T) {
	r := testRouter(t, nil, &stubCorpusStore{}, nil)
	w := doJSON(t, r, http.MethodGet, "/api/v1/corpora", "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", w.Code)
	}
}

func TestCreateCorpusProjectNotFound(t *testing.T) {
	r := testRouter(t, nil, &stubCorpusStore{createErr: store.ErrNotFound}, nil)
	w := doJSON(t, r, http.MethodPost, "/api/v1/corpora",
		`{"project_id":999,"name":"帮助文档"}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", w.Code)
	}
}

func TestCreateCorpusConflict(t *testing.T) {
	r := testRouter(t, nil, &stubCorpusStore{createErr: store.ErrConflict}, nil)
	w := doJSON(t, r, http.MethodPost, "/api/v1/corpora",
		`{"project_id":1,"name":"帮助文档"}`)
	if w.Code != http.StatusConflict {
		t.Errorf("code = %d, want 409", w.Code)
	}
}

func TestCreateDocumentsEmpty(t *testing.T) {
	r := testRouter(t, nil, nil, &stubDocumentStore{})
	w := doJSON(t, r, http.MethodPost, "/api/v1/corpora/1/documents", `{"documents":[]}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", w.Code)
	}
}

func TestCreateDocumentsDupInRequest(t *testing.T) {
	r := testRouter(t, nil, nil, &stubDocumentStore{})
	body := `{"documents":[
		{"doc_id":"A01","title":"t","raw_text":"x"},
		{"doc_id":"A01","title":"t2","raw_text":"y"}]}`
	w := doJSON(t, r, http.MethodPost, "/api/v1/corpora/1/documents", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", w.Code)
	}
}
