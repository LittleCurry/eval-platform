package store

import (
	"context"
	"database/sql"
	"errors"
)

// ListCorpora 按项目列出语料库。
func (p *Postgres) ListCorpora(ctx context.Context, projectID int64) ([]Corpus, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT id, project_id, name, source_type, created_by, created_at, updated_at
		FROM corpora WHERE project_id = $1 ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Corpus, 0)
	for rows.Next() {
		var c Corpus
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Name, &c.SourceType, &c.CreatedBy,
			&c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateCorpus 创建语料库; sourceType 为空时使用 manual。
// 项目不存在 → ErrNotFound; 同项目重名 → ErrConflict。
func (p *Postgres) CreateCorpus(ctx context.Context, projectID int64, name, sourceType string) (Corpus, error) {
	if sourceType == "" {
		sourceType = "manual"
	}
	var c Corpus
	err := p.db.QueryRowContext(ctx, `
		INSERT INTO corpora (project_id, name, source_type)
		VALUES ($1, $2, $3)
		RETURNING id, project_id, name, source_type, created_by, created_at, updated_at`,
		projectID, name, sourceType,
	).Scan(&c.ID, &c.ProjectID, &c.Name, &c.SourceType, &c.CreatedBy,
		&c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		switch {
		case isForeignKeyViolation(err):
			return Corpus{}, ErrNotFound // 外键失败 = project 不存在
		case isUniqueViolation(err):
			return Corpus{}, ErrConflict
		default:
			return Corpus{}, err
		}
	}
	return c, nil
}

// GetCorpus 按 id 取语料库。
func (p *Postgres) GetCorpus(ctx context.Context, id int64) (Corpus, error) {
	var c Corpus
	err := p.db.QueryRowContext(ctx, `
		SELECT id, project_id, name, source_type, created_by, created_at, updated_at
		FROM corpora WHERE id = $1`, id,
	).Scan(&c.ID, &c.ProjectID, &c.Name, &c.SourceType, &c.CreatedBy,
		&c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Corpus{}, ErrNotFound
	}
	if err != nil {
		return Corpus{}, err
	}
	return c, nil
}

// UpdateCorpus 局部更新 name/source_type; 对应指针传 nil 表示不改该字段。
func (p *Postgres) UpdateCorpus(ctx context.Context, id int64, name, sourceType *string) (Corpus, error) {
	var c Corpus
	err := p.db.QueryRowContext(ctx, `
		UPDATE corpora
		SET name        = COALESCE($2, name),
		    source_type = COALESCE($3, source_type),
		    updated_at  = now()
		WHERE id = $1
		RETURNING id, project_id, name, source_type, created_by, created_at, updated_at`,
		id, name, sourceType,
	).Scan(&c.ID, &c.ProjectID, &c.Name, &c.SourceType, &c.CreatedBy,
		&c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Corpus{}, ErrNotFound
	}
	if err != nil {
		return Corpus{}, err
	}
	return c, nil
}

// DeleteCorpus 删除语料库(文档随外键级联删除)。
func (p *Postgres) DeleteCorpus(ctx context.Context, id int64) error {
	res, err := p.db.ExecContext(ctx, `DELETE FROM corpora WHERE id = $1`, id)
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

// corpusExists 供文档相关方法校验语料库存在性。
func (p *Postgres) corpusExists(ctx context.Context, id int64) (bool, error) {
	var one int
	err := p.db.QueryRowContext(ctx, `SELECT 1 FROM corpora WHERE id = $1`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
