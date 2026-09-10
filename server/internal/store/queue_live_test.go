package store

import (
	"context"
	"os"
	"sync"
	"testing"
)

// live 集成测试: 用真实 PostgreSQL 验证队列语义(RUN_LIVE=1 才跑; 自建自清)。
//
// 重点验证(简历相关):
//  1. 并发领取任务互不重复 —— SELECT ... FOR UPDATE SKIP LOCKED 的核心价值;
//  2. 微任务领取不重叠;
//  3. CompleteJobItem 是原子 checkpoint(结果 + 状态 + 进度一起提交);
//  4. 失败重试回到 pending, 超上限进 failed(死信);
//  5. FinishJob 同时结束 job 与 run。

const (
	liveDatasetID int64 = 3
	liveCorpusID  int64 = 4
)

func livePostgres(t *testing.T) *Postgres {
	t.Helper()
	if os.Getenv("RUN_LIVE") != "1" {
		t.Skip("设置 RUN_LIVE=1 才连真实数据库")
	}
	dsn := os.Getenv("PG_DSN_TEST")
	if dsn == "" {
		dsn = "postgresql://eval:eval_dev_password@localhost:5432/eval_platform"
	}
	pg, err := NewPostgres(dsn)
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	return pg
}

func newLiveRun(t *testing.T, pg *Postgres) (RunJobRef, int64) {
	t.Helper()
	ctx := context.Background()
	projectID, err := pg.GetDatasetProject(ctx, liveDatasetID)
	if err != nil {
		t.Fatalf("数据集 %d 不存在: %v", liveDatasetID, err)
	}
	ref, err := pg.CreateRunWithJob(ctx, CreateRunInput{
		ProjectID:      projectID,
		DatasetID:      liveDatasetID,
		CorpusID:       liveCorpusID,
		ConfigSnapshot: map[string]any{"chunking": map[string]any{"strategy": "headings"}},
		ConfigHash:     "test-config-hash",
		GitSHA:         "test-sha",
	})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	t.Cleanup(func() { _ = pg.DeleteRun(context.Background(), ref.RunID) })
	return ref, projectID
}

func TestQueueCreateAndClaimLive(t *testing.T) {
	pg := livePostgres(t)
	ctx := context.Background()

	ref, _ := newLiveRun(t, pg)
	if ref.Items != 30 {
		t.Fatalf("job_items 应为评测集用例数 30, 实际 %d", ref.Items)
	}

	job, err := pg.ClaimJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if job == nil {
		t.Fatal("应能领到刚创建的任务")
	}
	if job.ID != ref.JobID || job.Status != "running" || job.RunID != ref.RunID {
		t.Fatalf("领取结果不符: %+v (期望 job %d)", job, ref.JobID)
	}
	if job.HeartbeatAt == nil {
		t.Fatal("领取后应写入心跳时间")
	}

	if err := pg.HeartbeatJob(ctx, job.ID); err != nil {
		t.Fatalf("刷新心跳失败: %v", err)
	}
}

func TestQueueConcurrentClaimNoDuplicateLive(t *testing.T) {
	pg := livePostgres(t)
	ctx := context.Background()

	// 造两个待领取任务, 让两个 goroutine 同时领
	refA, _ := newLiveRun(t, pg)
	refB, _ := newLiveRun(t, pg)

	var wg sync.WaitGroup
	claimed := make(chan int64, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, err := pg.ClaimJob(ctx)
			if err != nil || job == nil {
				return
			}
			claimed <- job.ID
		}()
	}
	wg.Wait()
	close(claimed)

	ids := make([]int64, 0, 2)
	for id := range claimed {
		ids = append(ids, id)
	}
	if len(ids) != 2 {
		t.Fatalf("两个待领任务应都被领取, 实际领到 %d 个: %v (A=%d B=%d)", len(ids), ids, refA.JobID, refB.JobID)
	}
	if ids[0] == ids[1] {
		t.Fatalf("并发领取不能拿到同一条任务: %v", ids)
	}
}

func TestQueueItemsCheckpointAndRetryLive(t *testing.T) {
	pg := livePostgres(t)
	ctx := context.Background()

	ref, _ := newLiveRun(t, pg)
	job, err := pg.ClaimJob(ctx)
	if err != nil || job == nil {
		t.Fatalf("领取任务失败: %v", err)
	}

	first, err := pg.ClaimJobItems(ctx, ref.JobID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 5 {
		t.Fatalf("应领到 5 条微任务, 实际 %d", len(first))
	}
	second, err := pg.ClaimJobItems(ctx, ref.JobID, 5)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, item := range first {
		seen[item.ID] = true
	}
	for _, item := range second {
		if seen[item.ID] {
			t.Fatalf("两次领取出现重复微任务: %d", item.ID)
		}
	}

	// 完成一条: 结果 + 状态 + 进度必须一起落库
	err = pg.CompleteJobItem(ctx, ref.JobID, first[0].ID, ref.RunID, CaseResultRow{
		CaseID:    first[0].CaseID,
		Retrieved: []map[string]any{{"point_id": "p1", "doc_id": "A01", "score": 0.9}},
		Metrics:   map[string]any{"recall": 1.0, "hit": 1.0},
		Flags:     []string{},
	})
	if err != nil {
		t.Fatalf("完成微任务失败: %v", err)
	}

	results, err := pg.ListRunCaseResults(ctx, ref.RunID, 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].CaseID != first[0].CaseID {
		t.Fatalf("case_results 未写入: %+v", results)
	}

	progress, err := pg.JobProgress(ctx, ref.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if progress["succeeded"] != 1 || progress["running"] != 9 || progress["pending"] != 20 || progress["total"] != 30 {
		t.Fatalf("进度统计不符: %+v", progress)
	}

	// 失败: 未超上限 -> 回到 pending 重试; 达到上限 -> failed(死信)
	retried, err := pg.FailJobItem(ctx, ref.JobID, second[0].ID, "模拟瞬时错误", 3)
	if err != nil {
		t.Fatal(err)
	}
	if !retried {
		t.Fatal("未达重试上限应重新入队")
	}
	dead, err := pg.FailJobItem(ctx, ref.JobID, second[1].ID, "模拟永久错误", 1)
	if err != nil {
		t.Fatal(err)
	}
	if dead {
		t.Fatal("达到重试上限应进入死信, 不再重试")
	}

	progress, _ = pg.JobProgress(ctx, ref.JobID)
	if progress["failed"] != 1 || progress["pending"] != 21 {
		t.Fatalf("重试/死信后进度不符: %+v", progress)
	}

	// 结束任务: job 与 run 同时进入终态
	if err := pg.FinishJob(ctx, ref.JobID, "failed", "部分用例失败"); err != nil {
		t.Fatal(err)
	}
	run, err := pg.GetRun(ctx, ref.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" || run.FinishedAt == nil {
		t.Fatalf("run 终态不符: status=%s finished=%v", run.Status, run.FinishedAt)
	}
	if run.Error == "" {
		t.Fatal("run.error 应记录失败原因")
	}
}

func TestQueueClaimWhenEmptyLive(t *testing.T) {
	pg := livePostgres(t)
	ctx := context.Background()

	// 把现有 pending 任务全部领走(仅本测试创建的 job 会被清理, 这里只是验证"领空返回 nil")
	for i := 0; i < 50; i++ {
		job, err := pg.ClaimJob(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if job == nil {
			return // 已经没有待领任务 -> 语义正确
		}
		if err := pg.FinishJob(ctx, job.ID, "failed", "测试清理: 空队列验证"); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("连续领取 50 次仍未出现空队列, 可能有任务泄漏")
}

// ---- M3-3: 僵尸任务接管(kill -9 恢复) ----

func TestReclaimStaleJobAndResumeLive(t *testing.T) {
	pg := livePostgres(t)
	ctx := context.Background()

	ref, _ := newLiveRun(t, pg)
	job, err := pg.ClaimJob(ctx)
	if err != nil || job == nil {
		t.Fatalf("领取任务失败: %v", err)
	}
	if job.ID != ref.JobID {
		t.Skip("队列中存在其他 pending 任务, 跳过本条(避免误接管他人任务)")
	}

	items, err := pg.ClaimJobItems(ctx, ref.JobID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("应领到 3 条, 实际 %d", len(items))
	}
	if err := pg.CompleteJobItem(ctx, ref.JobID, items[0].ID, ref.RunID, CaseResultRow{
		CaseID: items[0].CaseID, Retrieved: []map[string]any{}, Metrics: map[string]any{"recall": 1.0},
	}); err != nil {
		t.Fatal(err)
	}

	// 模拟 worker 被 kill -9: 心跳停在过去(而非等待真实超时)
	if _, err := pg.db.ExecContext(ctx,
		`UPDATE jobs SET heartbeat_at = now() - interval '10 minutes' WHERE id = $1`, ref.JobID); err != nil {
		t.Fatal(err)
	}

	// 活跃任务不应被接管(阈值 1 小时 > 心跳年龄 10 分钟)
	result, err := pg.ReclaimStaleJobs(ctx, 3600)
	if err != nil {
		t.Fatal(err)
	}
	if result.Jobs != 0 {
		t.Fatalf("阈值大于心跳年龄时不应接管, 实际 %+v", result)
	}

	// 阈值放开后被接管: running 微任务回到 pending, 任务回到 pending
	result, err = pg.ReclaimStaleJobs(ctx, 60)
	if err != nil {
		t.Fatal(err)
	}
	if result.Jobs == 0 {
		t.Fatal("心跳超时任务应被接管")
	}
	progress, err := pg.JobProgress(ctx, ref.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if progress["running"] != 0 {
		t.Fatalf("接管后不应残留 running 微任务: %+v", progress)
	}
	if progress["succeeded"] != 1 || int64(progress["pending"]) != ref.Items-1 {
		t.Fatalf("接管后进度不符(已完成的必须保留): %+v", progress)
	}
	job2, err := pg.ClaimJobByID(ctx, ref.JobID)
	if err != nil || job2 == nil {
		t.Fatalf("被接管的任务应可重新领取: %v", err)
	}
	if job2.Status != "running" {
		t.Fatalf("重新领取后状态应为 running: %+v", job2)
	}
}

func TestReclaimLeavesFreshJobAloneLive(t *testing.T) {
	pg := livePostgres(t)
	ctx := context.Background()

	ref, _ := newLiveRun(t, pg)
	job, err := pg.ClaimJob(ctx)
	if err != nil || job == nil {
		t.Fatalf("领取任务失败: %v", err)
	}
	if job.ID != ref.JobID {
		t.Skip("队列中存在其他 pending 任务, 跳过本条")
	}
	if err := pg.HeartbeatJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}

	result, err := pg.ReclaimStaleJobs(ctx, 60)
	if err != nil {
		t.Fatal(err)
	}
	if result.Jobs != 0 {
		t.Fatalf("刚心跳过的任务不应被接管: %+v", result)
	}
	still, err := pg.GetRun(ctx, ref.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if still.Status != "running" {
		t.Fatalf("活跃任务的 run 应保持 running: %s", still.Status)
	}
}
