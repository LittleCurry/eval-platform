package http

import (
	"context"

	"eval-platform/server/internal/store"
)

// 消费方接口: handler 只依赖这些小接口, 测试注入 fake 实现即可。

type ProjectStore interface {
	ListProjects(ctx context.Context) ([]store.Project, error)
	CreateProject(ctx context.Context, name, description string) (store.Project, error)
}

type CorpusStore interface {
	ListCorpora(ctx context.Context, projectID int64) ([]store.Corpus, error)
	CreateCorpus(ctx context.Context, projectID int64, name, sourceType string) (store.Corpus, error)
	GetCorpus(ctx context.Context, id int64) (store.Corpus, error)
	UpdateCorpus(ctx context.Context, id int64, name, sourceType *string) (store.Corpus, error)
	DeleteCorpus(ctx context.Context, id int64) error
}

type DocumentStore interface {
	ListDocuments(ctx context.Context, corpusID int64) ([]store.Document, error)
	CreateDocuments(ctx context.Context, corpusID int64, docs []store.Document) (int64, error)
	GetDocument(ctx context.Context, id int64) (store.Document, error)
	DeleteDocument(ctx context.Context, id int64) error
}
