package http

import (
	"eval-platform/server/internal/eval"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Deps 聚合 handler 所需依赖; store 以接口注入, 便于测试替换。
type Deps struct {
	Postgres       Pinger
	Qdrant         Pinger
	Projects       ProjectStore
	Corpora        CorpusStore
	Documents      DocumentStore
	Datasets       DatasetStore
	Cases          CaseStore
	Runs           RunStore
	QdrantPoints   QdrantPointStore
	Profiles       PipelineProfileStore
	Annotations    AnnotationStore
	HumanGold      HumanGoldStore
	EvalEmbedding  eval.EmbeddingConfig
	EvalGeneration eval.GenerationConfig
	EvalJudge      eval.JudgeConfig
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
	runs := newRunHandler(d.Runs, d.EvalEmbedding, d.EvalGeneration, d.EvalJudge)
	runContext := newRunContextHandler(d.Runs, d.QdrantPoints)
	compare := newCompareHandler(d.Runs)
	profiles := newPipelineProfileHandler(d.Profiles, d.EvalEmbedding, d.EvalGeneration, d.EvalJudge)
	annotations := newAnnotationHandler(d.Annotations, d.Runs)
	gold := newHumanGoldHandler(d.HumanGold, d.Runs)

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
	v1.GET("/runs/:id/cases/:case_id/context", runContext.CaseContext)
	// M5-1: A/B 对比(主语是"两次 run", 所以放在顶层, 也避开 /runs/:id 的路由树)
	v1.GET("/compare", compare.Compare)
	// M5-2: 配置模板 CRUD + 提交前预览指纹
	//   预览放顶层 /pipeline-preview: gin 同一层不允许静态段与 :id 通配段共存(会 panic)
	v1.GET("/pipeline-profiles", profiles.List)
	v1.POST("/pipeline-profiles", profiles.Create)
	v1.GET("/pipeline-profiles/:id", profiles.Get)
	v1.PATCH("/pipeline-profiles/:id", profiles.Update)
	v1.DELETE("/pipeline-profiles/:id", profiles.Delete)
	v1.POST("/pipeline-preview", profiles.Preview)
	// M6: Bad Case 标注(状态机在服务端) + 统计 + 归因建议
	//   /annotation-stats 与 /annotation-suggestion 同样放顶层: 避开 :id 通配段冲突
	v1.GET("/annotations", annotations.List)
	v1.POST("/annotations", annotations.Upsert)
	v1.PATCH("/annotations/:id", annotations.Update)
	v1.DELETE("/annotations/:id", annotations.Delete)
	v1.GET("/annotation-stats", annotations.Stats)
	v1.GET("/annotation-suggestion", annotations.Suggestion)
	// M6: 人工金标打分 + judge 校准报告
	//   金标挂在 run 上(judge 判的是"这次 run 生成的那段答案"), 换 run 就要重标
	//   /judge-calibration 放顶层: 同层不放静态段, 避免与 /human-gold/:id 的通配段冲突
	v1.GET("/human-gold", gold.List)
	v1.POST("/human-gold", gold.Upsert)
	v1.PATCH("/human-gold/:id", gold.Update)
	v1.DELETE("/human-gold/:id", gold.Delete)
	v1.GET("/judge-calibration", gold.Calibration)
	return r
}
