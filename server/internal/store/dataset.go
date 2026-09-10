package store

import (
	"context"
	"database/sql"
	"errors"
)

// ListDatasets 按项目列出数据集(带 case_count)。
func (p *Postgres) ListDatasets(ctx context.Context, projectID int64) ([]Dataset, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT d.id, d.project_id, d.name, d.description, d.created_at, d.updated_at,
		       (SELECT count(*) FROM cases c WHERE c.dataset_id = d.id) AS case_count
		FROM datasets d
		WHERE d.project_id = $1
		ORDER BY d.id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Dataset, 0)
	for rows.Next() {
		var ds Dataset
		if err := rows.Scan(&ds.ID, &ds.ProjectID, &ds.Name, &ds.Description,
			&ds.CreatedAt, &ds.UpdatedAt, &ds.CaseCount); err != nil {
			return nil, err
		}
		out = append(out, ds)
	}
	return out, rows.Err()
}

// CreateDataset 创建数据集; 项目不存在 → ErrNotFound; 同项目重名 → ErrConflict。
func (p *Postgres) CreateDataset(ctx context.Context, projectID int64, name, description string) (Dataset, error) {
	var ds Dataset
	err := p.db.QueryRowContext(ctx, `
		INSERT INTO datasets (project_id, name, description)
		VALUES ($1, $2, $3)
		RETURNING id, project_id, name, description, created_at, updated_at`,
		projectID, name, description,
	).Scan(&ds.ID, &ds.ProjectID, &ds.Name, &ds.Description,
		&ds.CreatedAt, &ds.UpdatedAt)
	if err != nil {
		switch {
		case isForeignKeyViolation(err):
			return Dataset{}, ErrNotFound
		case isUniqueViolation(err):
			return Dataset{}, ErrConflict
		default:
			return Dataset{}, err
		}
	}
	return ds, nil
}

// GetDataset 按 id 取数据集(带 case_count)。
func (p *Postgres) GetDataset(ctx context.Context, id int64) (Dataset, error) {
	var ds Dataset
	err := p.db.QueryRowContext(ctx, `
		SELECT d.id, d.project_id, d.name, d.description, d.created_at, d.updated_at,
		       (SELECT count(*) FROM cases c WHERE c.dataset_id = d.id) AS case_count
		FROM datasets d
		WHERE d.id = $1`, id,
	).Scan(&ds.ID, &ds.ProjectID, &ds.Name, &ds.Description,
		&ds.CreatedAt, &ds.UpdatedAt, &ds.CaseCount)
	if errors.Is(err, sql.ErrNoRows) {
		return Dataset{}, ErrNotFound
	}
	if err != nil {
		return Dataset{}, err
	}
	return ds, nil
}

// DeleteDataset 删除数据集(cases 随外键级联删除)。
func (p *Postgres) DeleteDataset(ctx context.Context, id int64) error {
	res, err := p.db.ExecContext(ctx, `DELETE FROM datasets WHERE id = $1`, id)
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

// GetDatasetProject 取数据集所属项目 id(提交评测任务时用于补全 project_id)。
func (p *Postgres) GetDatasetProject(ctx context.Context, id int64) (int64, error) {
	var projectID int64
	err := p.db.QueryRowContext(ctx, `SELECT project_id FROM datasets WHERE id = $1`, id).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	return projectID, nil
}

// datasetExists 供 case 相关方法校验数据集存在性。
func (p *Postgres) datasetExists(ctx context.Context, id int64) (bool, error) {
	var one int
	err := p.db.QueryRowContext(ctx, `SELECT 1 FROM datasets WHERE id = $1`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
