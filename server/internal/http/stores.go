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
	// GetRunCaseResult 取单题结果(M4-4.1: 抽屉按需取 chunk 正文前要先拿到该题的 retrieved)。
	GetRunCaseResult(ctx context.Context, runID, caseID int64) (store.RunCaseResult, error)
	ListRunFlagCounts(ctx context.Context, runID int64) (map[string]int, error)

	// 任务编排(M3)
	GetDatasetProject(ctx context.Context, datasetID int64) (int64, error)
	CreateRunWithJob(ctx context.Context, in store.CreateRunInput) (store.RunJobRef, error)
	ReclaimStaleJobs(ctx context.Context, olderThanSeconds float64) (store.ReclaimResult, error)
	GetJobByRun(ctx context.Context, runID int64) (*store.Job, error)
	JobProgress(ctx context.Context, jobID int64) (map[string]int, error)
}

// QdrantPointStore 按 id 取回 chunk 正文(M4-4.1)。
//
// 正文只在向量库里有一份, 不落库(process.md D17), 所以报告抽屉要展示上下文时
// 只能按需去取; 单独一个接口是为了让 handler 的测试能注入 fake, 不依赖真向量库。
type QdrantPointStore interface {
	RetrievePoints(ctx context.Context, collection string, ids []string) (map[string]store.QdrantPoint, error)
}
