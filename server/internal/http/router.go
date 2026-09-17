package http

import (
	"log"
	"net/http"

	"eval-platform/server/internal/auth"
	"eval-platform/server/internal/eval"

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
	// Auth 为 nil 表示**鉴权未启用**(仅测试与显式 AUTH_DISABLED=1 的本地开发):
	// 中间件放行并塞一个匿名 admin。生产路径上 main.go 会保证它非 nil ——
	// 把"忘了配密钥"挡在启动阶段, 而不是退化成一个默认放行的 router。
	Auth *auth.Service
}

// NewRouter 组装全部路由。
//
// 路由按**权限**分组, 而不是按资源分组: 这样"这个端点谁能用"在路由表上一眼可见,
// 新增端点时也会被迫选一个组 —— 漏选不会被静默放过, TestEveryAPIRouteIsGuarded 会报。
func NewRouter(d Deps) *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	mw := auth.NewMiddleware(d.Auth, d.Auth != nil)
	if !mw.Enabled() {
		log.Printf("⚠️  鉴权未启用(AUTH_DISABLED=1 或测试环境): 所有 /api/v1 接口不需登录 —— 不要这样部署")
	}

	health := newHealthHandler(d.Postgres, d.Qdrant)
	// 健康检查与首页必须免鉴权: 容器探针、反向代理、前端首屏都拿不到 token
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
	closure := newClosureHandler(d.Runs, d.Annotations)
	authAPI := newAuthHandler(d.Auth)
	users := newUserHandler(d.Auth)

	v1 := r.Group("/api/v1")

	// ---- 公开: 认证入口(登录页在未登录状态也要能调到) ----
	v1.GET("/auth/status", authAPI.Status)
	v1.POST("/auth/login", authAPI.Login)
	v1.POST("/auth/register", authAPI.Register)

	// ---- 需要登录(任意角色) ----
	authed := v1.Group("", mw.Required())
	authed.GET("/auth/me", authAPI.Me)
	authed.POST("/auth/logout", authAPI.Logout)

	// read: 所有已登录用户(含 viewer)
	read := authed.Group("")
	read.GET("/projects", projects.List)
	read.GET("/projects/:id", projects.Get)
	read.GET("/corpora", corpora.List)
	read.GET("/corpora/:id", corpora.Get)
	read.GET("/corpora/:id/documents", documents.List)
	read.GET("/documents/:id", documents.Get)
	read.GET("/datasets", datasets.List)
	read.GET("/datasets/:id", datasets.Get)
	read.GET("/datasets/:id/cases", cases.List)
	read.GET("/cases/:id", cases.Get)
	read.GET("/runs", runs.List)
	read.GET("/runs/:id", runs.Get)
	read.GET("/runs/:id/case-results", runs.CaseResults)
	read.GET("/runs/:id/report", runs.Report)
	read.GET("/runs/:id/progress", runs.Progress)
	read.GET("/runs/:id/cases/:case_id/context", runContext.CaseContext)
	read.GET("/compare", compare.Compare)
	read.GET("/pipeline-profiles", profiles.List)
	read.GET("/pipeline-profiles/:id", profiles.Get)
	read.GET("/annotations", annotations.List)
	read.GET("/annotation-stats", annotations.Stats)
	read.GET("/annotation-suggestion", annotations.Suggestion)
	read.GET("/human-gold", gold.List)
	read.GET("/judge-calibration", gold.Calibration)
	read.GET("/closure", closure.Closure)

	// write: editor 及以上 —— 建/改数据与人工结论(语料、数据集、标注、金标、配置模板)
	write := authed.Group("", mw.RequireAction(auth.ActionWrite))
	write.POST("/corpora", corpora.Create)
	write.PATCH("/corpora/:id", corpora.Update)
	write.POST("/datasets", datasets.Create)
	write.POST("/datasets/:id/cases/import", cases.Import)
	write.POST("/corpora/:id/documents", documents.Create)
	write.POST("/pipeline-profiles", profiles.Create)
	write.PATCH("/pipeline-profiles/:id", profiles.Update)
	write.DELETE("/pipeline-profiles/:id", profiles.Delete)
	write.POST("/pipeline-preview", profiles.Preview)
	write.POST("/annotations", annotations.Upsert)
	write.PATCH("/annotations/:id", annotations.Update)
	write.DELETE("/annotations/:id", annotations.Delete)
	write.POST("/human-gold", gold.Upsert)
	write.PATCH("/human-gold/:id", gold.Update)
	write.DELETE("/human-gold/:id", gold.Delete)

	// submit: 跑实验会花钱调 LLM, 单独一档(能看能改的人未必该能随便烧钱)
	submit := authed.Group("", mw.RequireAction(auth.ActionSubmit))
	submit.POST("/runs", runs.Submit)
	submit.POST("/jobs/reclaim", runs.Reclaim)

	// admin: 建项目/建数据集/管人 —— 影响他人或改变系统边界的动作
	admin := authed.Group("", mw.RequireAction(auth.ActionAdmin))
	admin.POST("/projects", projects.Create)
	admin.GET("/users", users.List)
	admin.POST("/users", users.Create)
	admin.PATCH("/users/:id", users.Update)
	admin.DELETE("/users/:id", users.Delete)

	// delete: 删数据不可逆, 只给 admin。与 admin 组能力相同但分开写 —— 语义不同,
	// 将来若要"editor 能删自己标的标注、但不能删数据集", 在这一组里单独调即可。
	remove := authed.Group("", mw.RequireAction(auth.ActionDelete))
	remove.DELETE("/corpora/:id", corpora.Delete)
	remove.DELETE("/documents/:id", documents.Delete)
	remove.DELETE("/datasets/:id", datasets.Delete)
	remove.DELETE("/cases/:id", cases.Delete)

	return r
}
