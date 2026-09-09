package store

import "context"

// ListProjects 返回全部项目(按 id 升序)。
func (p *Postgres) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT id, name, description, created_by, created_at, updated_at
		FROM projects ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Project, 0)
	for rows.Next() {
		var pr Project
		if err := rows.Scan(&pr.ID, &pr.Name, &pr.Description, &pr.CreatedBy,
			&pr.CreatedAt, &pr.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}

// CreateProject 创建项目; 重名返回 ErrConflict。
func (p *Postgres) CreateProject(ctx context.Context, name, description string) (Project, error) {
	var pr Project
	err := p.db.QueryRowContext(ctx, `
		INSERT INTO projects (name, description)
		VALUES ($1, $2)
		RETURNING id, name, description, created_by, created_at, updated_at`,
		name, description,
	).Scan(&pr.ID, &pr.Name, &pr.Description, &pr.CreatedBy,
		&pr.CreatedAt, &pr.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return Project{}, ErrConflict
		}
		return Project{}, err
	}
	return pr, nil
}
