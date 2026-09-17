package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// HumanGoldScore 对应 human_gold_scores 表(M6): 一个人对某次 run 某道题答案的人工判定。
//
// Verdict 三值: faithful / hallucinated / unclear —— "看不清"必须能表达,
// 否则标注员被迫二选一, κ 会被瞎猜污染。
// Relevance / Helpfulness 为 nil 表示"这题没打分"(只判幻觉是合法用法)。
type HumanGoldScore struct {
	ID          int64     `json:"id"`
	ProjectID   int64     `json:"project_id"`
	RunID       int64     `json:"run_id"`
	CaseID      int64     `json:"case_id"`
	Annotator   string    `json:"annotator"`
	Verdict     string    `json:"verdict"`
	Relevance   *int      `json:"relevance,omitempty"`
	Helpfulness *int      `json:"helpfulness,omitempty"`
	Note        string    `json:"note"`
	Reviewed    bool      `json:"reviewed"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// HumanGoldInput 新建/更新金标的输入(指针 = "不改该字段")。
//
// 为什么 Note 也是指针: 打分页常常是"一次只动一个字段"(先判有没有幻觉, 过后再补分/补备注)。
// 用 string 的零值当"没传", 第二次请求就会把上一次写的备注清成空串 —— M6-1 的标注
// upsert 恰好栽在这个坑里(只改状态, 统计里冒出一批 unclassified), 这里不重犯。
type HumanGoldInput struct {
	ProjectID   int64
	RunID       int64
	CaseID      int64
	Annotator   string
	Verdict     string
	Relevance   *int
	Helpfulness *int
	Note        *string
	Reviewed    *bool
}

const humanGoldColumns = `id, project_id, run_id, case_id, annotator, verdict,
	relevance, helpfulness, note, reviewed, created_at, updated_at`

// ListHumanGoldScores 列出某次 run 的人工金标(可按标注员过滤)。
func (p *Postgres) ListHumanGoldScores(
	ctx context.Context, runID int64, annotator string,
) ([]HumanGoldScore, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT `+humanGoldColumns+`
		FROM human_gold_scores
		WHERE ($1 = 0 OR run_id = $1)
		  AND ($2 = '' OR annotator = $2)
		ORDER BY case_id, annotator`, runID, annotator)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]HumanGoldScore, 0)
	for rows.Next() {
		item, err := scanHumanGold(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetHumanGoldScore 按 id 取一条金标。
func (p *Postgres) GetHumanGoldScore(ctx context.Context, id int64) (HumanGoldScore, error) {
	row := p.db.QueryRowContext(ctx, `
		SELECT `+humanGoldColumns+` FROM human_gold_scores WHERE id = $1`, id)
	item, err := scanHumanGold(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return HumanGoldScore{}, ErrNotFound
	}
	if err != nil {
		return HumanGoldScore{}, err
	}
	return item, nil
}

// UpsertHumanGoldScore 按 (run_id, case_id, annotator) 写入: 已有则更新。
//
// 为什么用 upsert 而不是"先查再决定 POST/PATCH": 打分页是连续录入的(一题一次),
// 标注员改主意是常态; 而"同一人同一题重复打分"本身没有语义。
//
// **冲突时是"合并"而不是"整行覆盖"**: verdict 一定被请求带上(必填), 直接覆盖;
// 分数/备注/复核标记只在请求带了值时才改。真机实测过一次整行覆盖的后果 ——
// 第二次只提交 verdict 时, 上一次打的分和备注全被 NULL/空串冲掉,
// 校准报告里那几对样本凭空消失, 而接口调用方看不出任何异常。
func (p *Postgres) UpsertHumanGoldScore(
	ctx context.Context, in HumanGoldInput,
) (HumanGoldScore, error) {
	row := p.db.QueryRowContext(ctx, `
		INSERT INTO human_gold_scores
			(project_id, run_id, case_id, annotator, verdict, relevance, helpfulness, note, reviewed)
		VALUES ($1, $2, $3, $4, $5, $6, $7, COALESCE($8::text, ''), COALESCE($9::boolean, false))
		ON CONFLICT (run_id, case_id, annotator) DO UPDATE
			SET verdict     = EXCLUDED.verdict,
			    relevance   = COALESCE(EXCLUDED.relevance, human_gold_scores.relevance),
			    helpfulness = COALESCE(EXCLUDED.helpfulness, human_gold_scores.helpfulness),
			    note        = COALESCE($8::text, human_gold_scores.note),
			    reviewed    = COALESCE($9::boolean, human_gold_scores.reviewed),
			    updated_at  = now()
		RETURNING `+humanGoldColumns,
		in.ProjectID, in.RunID, in.CaseID, in.Annotator, in.Verdict,
		in.Relevance, in.Helpfulness, in.Note, in.Reviewed)
	item, err := scanHumanGold(row.Scan)
	if err != nil {
		switch {
		case isForeignKeyViolation(err):
			return HumanGoldScore{}, ErrNotFound
		case isUniqueViolation(err):
			return HumanGoldScore{}, ErrConflict
		default:
			return HumanGoldScore{}, err
		}
	}
	return item, nil
}

// UpdateHumanGoldScore 局部更新; 指针传 nil 表示不改该字段。
//
// 分数字段多一个约定: **传 0 = 撤回这个分数(置 NULL)**。
// 原因: 合法分数是 1–5, 0 本身不是分数; 而 COALESCE 只能表达"不改"或"改成某个值",
// 表达不了"清空"。没有这个约定, 标注员打错分就只能删掉整条记录重来 ——
// 那会把同一行的 verdict 一起丢掉, 等于用"改一个分数"换来"重打一遍"。
func (p *Postgres) UpdateHumanGoldScore(
	ctx context.Context, id int64, verdict *string, relevance, helpfulness *int,
	note *string, reviewed *bool,
) (HumanGoldScore, error) {
	row := p.db.QueryRowContext(ctx, `
		UPDATE human_gold_scores
		SET verdict     = COALESCE($2, verdict),
		    relevance   = CASE WHEN $3::int IS NULL THEN relevance
		                       WHEN $3 = 0 THEN NULL
		                       ELSE $3 END,
		    helpfulness = CASE WHEN $4::int IS NULL THEN helpfulness
		                       WHEN $4 = 0 THEN NULL
		                       ELSE $4 END,
		    note        = COALESCE($5, note),
		    reviewed    = COALESCE($6, reviewed),
		    updated_at  = now()
		WHERE id = $1
		RETURNING `+humanGoldColumns,
		id, verdict, relevance, helpfulness, note, reviewed)
	item, err := scanHumanGold(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return HumanGoldScore{}, ErrNotFound
	}
	if err != nil {
		return HumanGoldScore{}, err
	}
	return item, nil
}

// DeleteHumanGoldScore 删除一条金标。
func (p *Postgres) DeleteHumanGoldScore(ctx context.Context, id int64) error {
	res, err := p.db.ExecContext(ctx, `DELETE FROM human_gold_scores WHERE id = $1`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanHumanGold(scan func(...any) error) (HumanGoldScore, error) {
	var item HumanGoldScore
	var relevance, helpfulness sql.NullInt64
	if err := scan(&item.ID, &item.ProjectID, &item.RunID, &item.CaseID, &item.Annotator,
		&item.Verdict, &relevance, &helpfulness, &item.Note, &item.Reviewed,
		&item.CreatedAt, &item.UpdatedAt); err != nil {
		return HumanGoldScore{}, err
	}
	if relevance.Valid {
		value := int(relevance.Int64)
		item.Relevance = &value
	}
	if helpfulness.Valid {
		value := int(helpfulness.Int64)
		item.Helpfulness = &value
	}
	return item, nil
}
