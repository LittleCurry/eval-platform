package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Annotation 对应 annotations 表(M6): 一条人工标注。
//
// Status 的合法取值: open / fixed / verified / wontfix(DB 有 CHECK 兜底);
// Reason 是人工确认的归因, 取值与 M4-3 的标签族对齐: retrieval / hallucination /
// generation / dataset / unknown, 空串表示"还没归类"。
type Annotation struct {
	ID        int64     `json:"id"`
	ProjectID int64     `json:"project_id"`
	RunID     int64     `json:"run_id"`
	CaseID    int64     `json:"case_id"`
	Status    string    `json:"status"`
	Reason    string    `json:"reason"`
	Comment   string    `json:"comment"`
	Assignee  string    `json:"assignee"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AnnotationFilter 列表过滤条件(零值 = 不过滤)。
type AnnotationFilter struct {
	RunID  int64
	CaseID int64
	Status string
	Limit  int
}

// AnnotationStats 标注统计(按状态/按归因), 供报告页与闭环页展示"这批 bad case 推到哪一步了"。
type AnnotationStats struct {
	Total    int            `json:"total"`
	ByStatus map[string]int `json:"by_status"`
	ByReason map[string]int `json:"by_reason"`
}

const annotationColumns = `id, project_id, run_id, case_id, status, reason,
	comment, assignee, created_by, created_at, updated_at`

// ListAnnotations 按过滤条件列出标注(按 id 升序, 便于工作台稳定翻页)。
func (p *Postgres) ListAnnotations(ctx context.Context, f AnnotationFilter) ([]Annotation, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 200
	}
	rows, err := p.db.QueryContext(ctx, `
		SELECT `+annotationColumns+`
		FROM annotations
		WHERE ($1 = 0 OR run_id = $1)
		  AND ($2 = 0 OR case_id = $2)
		  AND ($3 = '' OR status = $3)
		ORDER BY id
		LIMIT $4`, f.RunID, f.CaseID, f.Status, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Annotation, 0)
	for rows.Next() {
		var a Annotation
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.RunID, &a.CaseID, &a.Status, &a.Reason,
			&a.Comment, &a.Assignee, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAnnotationByRunCase 取某次 run 里某道题的标注; 没有则 ErrNotFound。
// 工作台据此判断"是新建还是更新", 状态机校验才有旧状态可依据。
func (p *Postgres) GetAnnotationByRunCase(ctx context.Context, runID, caseID int64) (Annotation, error) {
	var a Annotation
	err := p.db.QueryRowContext(ctx, `
		SELECT `+annotationColumns+`
		FROM annotations WHERE run_id = $1 AND case_id = $2`, runID, caseID,
	).Scan(&a.ID, &a.ProjectID, &a.RunID, &a.CaseID, &a.Status, &a.Reason,
		&a.Comment, &a.Assignee, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Annotation{}, ErrNotFound
	}
	if err != nil {
		return Annotation{}, err
	}
	return a, nil
}

// CreateAnnotation 新建标注; (run_id, case_id) 已存在 → ErrConflict, 外键失败 → ErrNotFound。
func (p *Postgres) CreateAnnotation(
	ctx context.Context, projectID, runID, caseID int64,
	status, reason, comment, assignee, createdBy string,
) (Annotation, error) {
	var a Annotation
	err := p.db.QueryRowContext(ctx, `
		INSERT INTO annotations (project_id, run_id, case_id, status, reason, comment, assignee, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+annotationColumns,
		projectID, runID, caseID, status, reason, comment, assignee, createdBy,
	).Scan(&a.ID, &a.ProjectID, &a.RunID, &a.CaseID, &a.Status, &a.Reason,
		&a.Comment, &a.Assignee, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		switch {
		case isUniqueViolation(err):
			return Annotation{}, ErrConflict
		case isForeignKeyViolation(err):
			return Annotation{}, ErrNotFound
		default:
			return Annotation{}, err
		}
	}
	return a, nil
}

// UpdateAnnotation 局部更新; 指针传 nil 表示不改该字段(空串是"显式清空", 与 nil 不同)。
func (p *Postgres) UpdateAnnotation(
	ctx context.Context, id int64, status, reason, comment, assignee *string,
) (Annotation, error) {
	var a Annotation
	err := p.db.QueryRowContext(ctx, `
		UPDATE annotations
		SET status     = COALESCE($2, status),
		    reason     = COALESCE($3, reason),
		    comment    = COALESCE($4, comment),
		    assignee   = COALESCE($5, assignee),
		    updated_at = now()
		WHERE id = $1
		RETURNING `+annotationColumns,
		id, status, reason, comment, assignee,
	).Scan(&a.ID, &a.ProjectID, &a.RunID, &a.CaseID, &a.Status, &a.Reason,
		&a.Comment, &a.Assignee, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Annotation{}, ErrNotFound
	}
	if err != nil {
		return Annotation{}, err
	}
	return a, nil
}

// DeleteAnnotation 删除标注(标错了就删掉重来, 比留一条假状态干净)。
func (p *Postgres) DeleteAnnotation(ctx context.Context, id int64) error {
	res, err := p.db.ExecContext(ctx, `DELETE FROM annotations WHERE id = $1`, id)
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

// AggregateAnnotationStats 统计某次 run 的标注状态与归因分布。
// 用两条 GROUP BY 查询而不是把全部行拉回来自己数: 标注量大时前者是常量级内存。
func (p *Postgres) AggregateAnnotationStats(ctx context.Context, runID int64) (AnnotationStats, error) {
	stats := AnnotationStats{
		ByStatus: map[string]int{},
		ByReason: map[string]int{},
	}
	rows, err := p.db.QueryContext(ctx, `
		SELECT status, count(*)::int FROM annotations WHERE run_id = $1 GROUP BY status`, runID)
	if err != nil {
		return AnnotationStats{}, err
	}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			return AnnotationStats{}, err
		}
		stats.ByStatus[status] = count
		stats.Total += count
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return AnnotationStats{}, err
	}

	rows, err = p.db.QueryContext(ctx, `
		SELECT reason, count(*)::int FROM annotations WHERE run_id = $1 GROUP BY reason`, runID)
	if err != nil {
		return AnnotationStats{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var reason string
		var count int
		if err := rows.Scan(&reason, &count); err != nil {
			return AnnotationStats{}, err
		}
		if strings.TrimSpace(reason) == "" {
			reason = "unclassified" // 空串在 JSON 里会被读成"没有这个键", 给个显式名字更好读
		}
		stats.ByReason[reason] = count
	}
	return stats, rows.Err()
}
