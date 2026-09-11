package http

import (
	"eval-platform/server/internal/eval"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Deps 聚合 handler 所需依赖; store 以接口注入, 便于测试替换。
type Deps struct {
	Postgres      Pinger
	Qdrant        Pinger
	Projects      ProjectStore
	Corpora       CorpusStore
	Documents     DocumentStore
	Datasets      DatasetStore
	Cases         CaseStore
	Runs          RunStore
	EvalEmbedding eval.EmbeddingConfig
}

// NewRouter 组装全部路由。
func NewRouter(d Deps) *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	health := newHealthHandler(d.Postgres, d.Qdrant)
	r.GET("/healthz", health.Healthz)
	r.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"service": "eval-platform", "docs": "/healthz"})
	})

	projects := newProjectHandler(d.Projects)
	corpora := newCorpusHandler(d.Corpora)
	documents := newDocumentHandler(d.Documents)
	datasets := newDatasetHandler(d.Datasets)
	cases := newCaseHandler(d.Cases)
	runs := newRunHandler(d.Runs, d.EvalEmbedding)

	v1 := r.Group("/api/v1")
	v1.GET("/projects", projects.List)
	v1.POST("/projects", projects.Create)
	v1.GET("/corpora", corpora.List)
	v1.POST("/corpora", corpora.Create)
	v1.GET("/corpora/:id", corpora.Get)
	v1.PATCH("/corpora/:id", corpora.Update)
	v1.DELETE("/corpora/:id", corpora.Delete)
	v1.POST("/corpora/:id/documents", documents.Create)
	v1.GET("/corpora/:id/documents", documents.List)
	v1.GET("/documents/:id", documents.Get)
	v1.DELETE("/documents/:id", documents.Delete)
	v1.GET("/datasets", datasets.List)
	v1.POST("/datasets", datasets.Create)
	v1.GET("/datasets/:id", datasets.Get)
	v1.DELETE("/datasets/:id", datasets.Delete)
	v1.POST("/datasets/:id/cases/import", cases.Import)
	v1.GET("/datasets/:id/cases", cases.List)
	v1.GET("/cases/:id", cases.Get)
	v1.DELETE("/cases/:id", cases.Delete)
	v1.POST("/runs", runs.Submit)
	v1.POST("/jobs/reclaim", runs.Reclaim)
	v1.GET("/runs", runs.List)
	v1.GET("/runs/:id", runs.Get)
	v1.GET("/runs/:id/case-results", runs.CaseResults)
	v1.GET("/runs/:id/report", runs.Report)
	v1.GET("/runs/:id/progress", runs.Progress)
	return r
}
