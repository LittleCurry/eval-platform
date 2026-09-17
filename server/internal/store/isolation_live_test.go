package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
)

// liveProjectFixture 造两个隔离的项目(各自一个语料 + 一个数据集), 用完自清。
//
// 为什么必须用真库测这条规则: "数据集与语料同项目"是**写入时的事务内不变量**,
// 它靠的是同一个事务里的两条 SELECT 加一次比较 —— stub 永远测不出
// "校验与插入之间被别人改掉"这类问题。
type liveProjectFixture struct {
	projectA int64
	projectB int64
	corpusA  int64
	corpusB  int64
	datasetA int64
	datasetB int64
	userID   int64
	note     string
}

func newLiveProjectFixture(t *testing.T, pg *Postgres) *liveProjectFixture {
	t.Helper()
	ctx := context.Background()
	note := fmt.Sprintf("m72-live-%d", os.Getpid())

	// created_by 有外键指向 users: 建一个临时账号来证明"归属真的落了库"
	var userID int64
	if err := pg.db.QueryRowContext(ctx, `
		INSERT INTO users (email, name, password_hash, role)
		VALUES ($1, '隔离测试', '$2a$10$not-a-real-hash-but-shaped-like-one-000000000000000000',
		        'editor')
		RETURNING id`, note+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("建临时账号失败: %v", err)
	}

	fixture := &liveProjectFixture{userID: userID, note: note}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		// 顺序: 先删子表(数据集/语料)再删项目, 最后删账号
		for _, id := range []int64{fixture.datasetA, fixture.datasetB} {
			if id > 0 {
				_, _ = pg.db.ExecContext(cleanupCtx, `DELETE FROM datasets WHERE id = $1`, id)
			}
		}
		for _, id := range []int64{fixture.corpusA, fixture.corpusB} {
			if id > 0 {
				_, _ = pg.db.ExecContext(cleanupCtx, `DELETE FROM corpora WHERE id = $1`, id)
			}
		}
		for _, id := range []int64{fixture.projectA, fixture.projectB} {
			if id > 0 {
				_, _ = pg.db.ExecContext(cleanupCtx, `DELETE FROM projects WHERE id = $1`, id)
			}
		}
		_, _ = pg.db.ExecContext(cleanupCtx, `DELETE FROM users WHERE id = $1`, fixture.userID)
	})

	projectA, err := pg.CreateProject(ctx, note+"-A", "隔离测试项目 A", userID)
	if err != nil {
		t.Fatalf("建项目 A 失败: %v", err)
	}
	fixture.projectA = projectA.ID
	projectB, err := pg.CreateProject(ctx, note+"-B", "隔离测试项目 B", userID)
	if err != nil {
		t.Fatalf("建项目 B 失败: %v", err)
	}
	fixture.projectB = projectB.ID

	corpusA, err := pg.CreateCorpus(ctx, fixture.projectA, "语料 A", "manual", userID)
	if err != nil {
		t.Fatalf("建语料 A 失败: %v", err)
	}
	fixture.corpusA = corpusA.ID
	corpusB, err := pg.CreateCorpus(ctx, fixture.projectB, "语料 B", "manual", userID)
	if err != nil {
		t.Fatalf("建语料 B 失败: %v", err)
	}
	fixture.corpusB = corpusB.ID

	datasetA, err := pg.CreateDataset(ctx, fixture.projectA, "数据集 A", "", userID)
	if err != nil {
		t.Fatalf("建数据集 A 失败: %v", err)
	}
	fixture.datasetA = datasetA.ID
	datasetB, err := pg.CreateDataset(ctx, fixture.projectB, "数据集 B", "", userID)
	if err != nil {
		t.Fatalf("建数据集 B 失败: %v", err)
	}
	fixture.datasetB = datasetB.ID

	return fixture
}

// TestLiveOwnershipRecorded 归属必须真的落库, 并且能读出"谁建的"。
func TestLiveOwnershipRecorded(t *testing.T) {
	pg := livePostgres(t)
	ctx := context.Background()
	fixture := newLiveProjectFixture(t, pg)

	project, err := pg.GetProject(ctx, fixture.projectA)
	if err != nil {
		t.Fatalf("取项目失败: %v", err)
	}
	if project.CreatedBy == nil || *project.CreatedBy != fixture.userID {
		t.Fatalf("项目应记下创建者: %+v", project)
	}
	if project.OwnerEmail != fixture.note+"@example.com" {
		t.Fatalf("项目应带出创建者邮箱(出事要找得到人): %q", project.OwnerEmail)
	}

	list, err := pg.ListProjects(ctx)
	if err != nil {
		t.Fatalf("列项目失败: %v", err)
	}
	found := false
	for _, item := range list {
		if item.ID == fixture.projectA {
			found = true
			if item.OwnerEmail == "" {
				t.Fatalf("列表里也要带 owner 邮箱: %+v", item)
			}
		}
	}
	if !found {
		t.Fatal("列表里找不到刚建的项目")
	}

	// 匿名(createdBy=0)必须落成 NULL, 而不是写一个不存在的用户 0
	anonymous, err := pg.CreateProject(ctx, fixture.note+"-anon", "匿名建的项目", 0)
	if err != nil {
		t.Fatalf("匿名建项目失败(0 应落成 NULL): %v", err)
	}
	t.Cleanup(func() {
		_, _ = pg.db.ExecContext(context.Background(), `DELETE FROM projects WHERE id = $1`, anonymous.ID)
	})
	if anonymous.CreatedBy != nil {
		t.Fatalf("匿名创建的 created_by 应为 NULL: %+v", anonymous)
	}

	// 数据集与语料同样要留痕
	dataset, err := pg.GetDataset(ctx, fixture.datasetA)
	if err != nil {
		t.Fatalf("取数据集失败: %v", err)
	}
	if dataset.CreatedBy == nil || *dataset.CreatedBy != fixture.userID {
		t.Fatalf("数据集应记下创建者: %+v", dataset)
	}
	corpora, err := pg.ListCorpora(ctx, fixture.projectA)
	if err != nil {
		t.Fatalf("列语料失败: %v", err)
	}
	if len(corpora) != 1 || corpora[0].CreatedBy == nil || *corpora[0].CreatedBy != fixture.userID {
		t.Fatalf("语料应记下创建者: %+v", corpora)
	}

	// 列表查询也要能跑通并带上归属 —— 这条断言曾经拦住一个真 bug:
	// SELECT 里加了 created_by 但 Scan 没跟着加, 列数不匹配会在运行时报错
	datasets, err := pg.ListDatasets(ctx, fixture.projectA)
	if err != nil {
		t.Fatalf("列数据集失败: %v", err)
	}
	if len(datasets) != 1 {
		t.Fatalf("项目 A 下应只有本次测试建的数据集: %+v", datasets)
	}
	if datasets[0].CreatedBy == nil || *datasets[0].CreatedBy != fixture.userID {
		t.Fatalf("数据集列表应带创建者: %+v", datasets[0])
	}
	if datasets[0].CaseCount != 0 {
		t.Fatalf("新数据集 case_count 应为 0: %+v", datasets[0])
	}
}

// TestLiveRunRejectsCrossProject 跨项目混用必须在事务里被拦住。
func TestLiveRunRejectsCrossProject(t *testing.T) {
	pg := livePostgres(t)
	ctx := context.Background()
	fixture := newLiveProjectFixture(t, pg)

	// 数据集 A + 语料 B = 跨项目
	_, err := pg.CreateRunWithJob(ctx, CreateRunInput{
		ProjectID: fixture.projectA,
		DatasetID: fixture.datasetA,
		CorpusID:  fixture.corpusB,
		ConfigSnapshot: map[string]any{"data": map[string]any{
			"dataset_id": fixture.datasetA, "corpus_id": fixture.corpusB,
		}},
		ConfigHash: "live-cross-project",
		GitSHA:     "test",
	})
	if !errors.Is(err, ErrProjectMismatch) {
		t.Fatalf("跨项目应报 ErrProjectMismatch: %v", err)
	}

	// 数据来源同项目, 但显式 project_id 指向另一个项目 -> 同样拒绝
	_, err = pg.CreateRunWithJob(ctx, CreateRunInput{
		ProjectID: fixture.projectB,
		DatasetID: fixture.datasetA,
		CorpusID:  fixture.corpusA,
		ConfigSnapshot: map[string]any{"data": map[string]any{
			"dataset_id": fixture.datasetA, "corpus_id": fixture.corpusA,
		}},
		ConfigHash: "live-wrong-project",
		GitSHA:     "test",
	})
	if !errors.Is(err, ErrProjectMismatch) {
		t.Fatalf("project_id 与数据来源不一致也要拒绝: %v", err)
	}

	// 拒绝之后不该留下任何 run 残骸(事务回滚)
	var leaked int
	if err := pg.db.QueryRowContext(ctx,
		`SELECT count(*) FROM runs WHERE config_hash IN ('live-cross-project', 'live-wrong-project')`).
		Scan(&leaked); err != nil {
		t.Fatalf("统计残留失败: %v", err)
	}
	if leaked != 0 {
		t.Fatalf("被拒绝的提交不该留下 run 记录: %d", leaked)
	}

	// 数据集不存在 -> ErrNotFound(而不是 mismatch)
	_, err = pg.CreateRunWithJob(ctx, CreateRunInput{
		DatasetID: 999999999, CorpusID: fixture.corpusA,
		ConfigSnapshot: map[string]any{}, ConfigHash: "live-missing", GitSHA: "test",
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("数据集不存在应报 ErrNotFound: %v", err)
	}

	// 语料不存在 -> ErrNotFound
	_, err = pg.CreateRunWithJob(ctx, CreateRunInput{
		DatasetID: fixture.datasetA, CorpusID: 999999999,
		ConfigSnapshot: map[string]any{}, ConfigHash: "live-missing-corpus", GitSHA: "test",
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("语料不存在应报 ErrNotFound: %v", err)
	}
}

// TestLiveRunProjectDerivedFromDataset project_id 传 0 时应由数据集推导并写进 runs。
func TestLiveRunProjectDerivedFromDataset(t *testing.T) {
	pg := livePostgres(t)
	ctx := context.Background()
	fixture := newLiveProjectFixture(t, pg)

	// 数据集 A 里得先有一道题, 否则会被"评测集没有用例"挡住
	if _, err := pg.db.ExecContext(ctx, `
		INSERT INTO cases (dataset_id, qid, question, gold_anchors)
		VALUES ($1, $2, '隔离测试题', '[]'::jsonb)`,
		fixture.datasetA, fixture.note+"-q1"); err != nil {
		t.Fatalf("建用例失败: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pg.db.ExecContext(context.Background(), `DELETE FROM cases WHERE dataset_id = $1`, fixture.datasetA)
	})

	// 即使调用方没传 project_id, 事务里也会把它补成数据集所属项目
	ref, err := pg.CreateRunWithJob(ctx, CreateRunInput{
		DatasetID: fixture.datasetA, CorpusID: fixture.corpusA,
		ConfigSnapshot: map[string]any{"data": map[string]any{"dataset_id": fixture.datasetA}},
		ConfigHash:     "live-derived-project", GitSHA: "test",
	})
	if err != nil {
		t.Fatalf("同项目的提交应成功: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pg.db.ExecContext(cleanupCtx, `DELETE FROM jobs WHERE run_id = $1`, ref.RunID)
		_, _ = pg.db.ExecContext(cleanupCtx, `DELETE FROM runs WHERE id = $1`, ref.RunID)
	})

	run, err := pg.GetRun(ctx, ref.RunID)
	if err != nil {
		t.Fatalf("取 run 失败: %v", err)
	}
	if run.ProjectID != fixture.projectA {
		t.Fatalf("run 的 project_id 应由数据集推导: %d vs %d", run.ProjectID, fixture.projectA)
	}
}
