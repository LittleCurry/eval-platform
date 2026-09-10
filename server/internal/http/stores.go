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

type DatasetStore interface {
	ListDatasets(ctx context.Context, projectID int64) ([]store.Dataset, error)
	CreateDataset(ctx context.Context, projectID int64, name, description string) (store.Dataset, error)
	GetDataset(ctx context.Context, id int64) (store.Dataset, error)
	DeleteDataset(ctx context.Context, id int64) error
}

type CaseStore interface {
	ListCases(ctx context.Context, datasetID int64) ([]store.Case, error)
	GetCase(ctx context.Context, id int64) (store.Case, error)
	DeleteCase(ctx context.Context, id int64) error
	ImportValidCases(ctx context.Context, datasetID int64, inputs []store.CaseInput) (store.CaseImportResult, error)
}

// RunStore 是评测运行结果的只读访问 + 任务提交(报告 API 与 M3 任务编排用)。
type RunStore interface {
	ListRuns(ctx context.Context, datasetID, projectID int64, limit int) ([]store.Run, error)
	GetRun(ctx context.Context, id int64) (store.Run, error)
	ListRunCaseResults(ctx context.Context, runID int64, limit int, flaggedOnly bool) ([]store.RunCaseResult, error)
	ListRunFlagCounts(ctx context.Context, runID int64) (map[string]int, error)

	// 任务编排(M3)
	GetDatasetProject(ctx context.Context, datasetID int64) (int64, error)
	CreateRunWithJob(ctx context.Context, in store.CreateRunInput) (store.RunJobRef, error)
}
