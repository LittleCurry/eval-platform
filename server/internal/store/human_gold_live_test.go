package store

import (
	"context"
	"errors"
	"testing"
)

// liveHumanGoldFixture 找一组真实存在的 (project, run, case) 供金标测试使用。
//
// 金标表有 4 个外键(project/run/case), 用假 id 连不上, 所以直接借现有 run 的一条结果。
// 测试自建自清: 只碰自己插入的行, 不依赖也不破坏已有数据。
func liveHumanGoldFixture(t *testing.T, pg *Postgres) (projectID, runID, caseID int64) {
	t.Helper()
	ctx := context.Background()
	err := pg.db.QueryRowContext(ctx, `
		SELECT r.project_id, cr.run_id, cr.case_id
		FROM case_results cr
		JOIN runs r ON r.id = cr.run_id
		ORDER BY cr.run_id DESC, cr.case_id
		LIMIT 1`).Scan(&projectID, &runID, &caseID)
	if err != nil {
		t.Fatalf("找不到可用的 (project, run, case) 组合, 先跑一次 run: %v", err)
	}
	return projectID, runID, caseID
}

// TestLiveHumanGoldUpsertAndClear 用真实 PostgreSQL 验证金标的写入语义。
//
// 这里必须用真库的理由: 单测的 stub 是我自己写的 SQL 语义近似, 但这两处只有真库能证明:
//  1. ON CONFLICT (run_id, case_id, annotator) 的冲突目标是否与唯一约束真的对得上
//     (写错列顺序时 Postgres 会直接报 "no unique or exclusion constraint matching");
//  2. PATCH 里"传 0 = 撤回分数"的 CASE WHEN 是否真的把列置成了 NULL 而不是 0
//     —— 0 会被 CHECK (relevance BETWEEN 1 AND 5) 拒掉, 这个 bug 只有真库会暴露。
func TestLiveHumanGoldUpsertAndClear(t *testing.T) {
	pg := livePostgres(t)
	ctx := context.Background()
	projectID, runID, caseID := liveHumanGoldFixture(t, pg)

	annotator := "live-test-annotator"
	cleanup := func() {
		if _, err := pg.db.ExecContext(ctx,
			`DELETE FROM human_gold_scores WHERE annotator = $1`, annotator); err != nil {
			t.Fatalf("清理测试数据失败: %v", err)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	annotatorValue := "code"

	relevance, helpfulness := 3, 2
	note := "首次打分"
	created, err := pg.UpsertHumanGoldScore(ctx, HumanGoldInput{
		ProjectID: projectID, RunID: runID, CaseID: caseID, Annotator: annotator,
		Verdict: "hallucinated", Relevance: &relevance, Helpfulness: &helpfulness,
		Note: &note,
	})
	if err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	if created.ID == 0 || created.Relevance == nil || *created.Relevance != 3 {
		t.Fatalf("写入结果不符: %+v", created)
	}

	// 同键再写一次 = 覆盖(不新增行)
	again, err := pg.UpsertHumanGoldScore(ctx, HumanGoldInput{
		ProjectID: projectID, RunID: runID, CaseID: caseID, Annotator: annotator,
		Verdict: "faithful", Relevance: &relevance,
	})
	if err != nil {
		t.Fatalf("覆盖写入失败: %v", err)
	}
	if again.ID != created.ID {
		t.Fatalf("同一 (run, case, annotator) 应覆盖同一行: %d vs %d", again.ID, created.ID)
	}

	rows, err := pg.ListHumanGoldScores(ctx, runID, annotator)
	if err != nil {
		t.Fatalf("列表查询失败: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("同键应只有一行: %d", len(rows))
	}
	if rows[0].Verdict != "faithful" || rows[0].Note != "首次打分" {
		t.Fatalf("覆盖后字段不符(没传的字段应保留): %+v", rows[0])
	}
	if rows[0].Helpfulness == nil || *rows[0].Helpfulness != 2 {
		t.Fatalf("没传 helpfulness 应保留原值: %+v", rows[0].Helpfulness)
	}

	// 另一位标注员 → 独立一行(双人一致性的前提)
	other := "live-test-annotator-2"
	if _, err := pg.db.ExecContext(ctx, `DELETE FROM human_gold_scores WHERE annotator = $1`, other); err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pg.db.ExecContext(ctx, `DELETE FROM human_gold_scores WHERE annotator = $1`, other)
	})
	if _, err := pg.UpsertHumanGoldScore(ctx, HumanGoldInput{
		ProjectID: projectID, RunID: runID, CaseID: caseID, Annotator: other, Verdict: "unclear",
	}); err != nil {
		t.Fatalf("第二位标注员写入失败: %v", err)
	}
	all, err := pg.ListHumanGoldScores(ctx, runID, "")
	if err != nil {
		t.Fatalf("全量列表失败: %v", err)
	}
	matched := 0
	for _, item := range all {
		if item.CaseID == caseID && (item.Annotator == annotator || item.Annotator == other) {
			matched++
		}
	}
	if matched != 2 {
		t.Fatalf("两位标注员应有两条记录: %d", matched)
	}

	// 撤回分数: 传 0 必须置 NULL, 而不是写 0(0 会被 CHECK 约束拒掉)
	zero := 0
	verdict := "faithful"
	updated, err := pg.UpdateHumanGoldScore(ctx, created.ID, &verdict, &zero, nil,
		&annotatorValue, nil)
	if err != nil {
		t.Fatalf("撤回分数失败(0 应被解释为清空, 不是写 0): %v", err)
	}
	if updated.Relevance != nil {
		t.Fatalf("撤回后 relevance 应为 NULL: %v", *updated.Relevance)
	}
	if updated.Note != "code" {
		t.Fatalf("备注更新未生效: %q", updated.Note)
	}
	if updated.Helpfulness == nil || *updated.Helpfulness != 2 {
		t.Fatalf("没传的分数不该被动: %+v", updated.Helpfulness)
	}

	// 局部更新语义: 全是 nil(不改)也必须成功(COALESCE 分支)
	if _, err := pg.UpdateHumanGoldScore(ctx, created.ID, nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("全 nil 更新应是无操作而非报错: %v", err)
	}

	// DB 层护栏: 越界分数被 CHECK 拒绝(handler 之外的第二道防线)
	bad := 6
	if _, err := pg.UpdateHumanGoldScore(ctx, created.ID, nil, &bad, nil, nil, nil); err == nil {
		t.Fatal("relevance=6 应被 CHECK 约束拒绝")
	}

	if err := pg.DeleteHumanGoldScore(ctx, created.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if err := pg.DeleteHumanGoldScore(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复删除应返回 ErrNotFound: %v", err)
	}
}
