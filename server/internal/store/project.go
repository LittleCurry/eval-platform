package store

import (
	"context"
	"database/sql"
	"errors"
)

// projectColumns 统一带 owner 邮箱(LEFT JOIN: 创建者账号被删后 created_by 变 NULL,
// 项目本身不该因此查不出来)。
const projectColumns = `p.id, p.name, p.description, p.created_by,
	COALESCE(u.email, ''), p.created_at, p.updated_at`
const projectFrom = `FROM projects p LEFT JOIN users u ON u.id = p.created_by`

// ListProjects 返回全部项目(按 id 升序)。
func (p *Postgres) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT `+projectColumns+`
		`+projectFrom+`
		ORDER BY p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Project, 0)
	for rows.Next() {
		var pr Project
		if err := rows.Scan(&pr.ID, &pr.Name, &pr.Description, &pr.CreatedBy,
			&pr.OwnerEmail, &pr.CreatedAt, &pr.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}

// GetProject 按 id 取项目(前端项目切换器与归属校验都要用)。
func (p *Postgres) GetProject(ctx context.Context, id int64) (Project, error) {
	row := p.db.QueryRowContext(ctx,
		`SELECT `+projectColumns+` `+projectFrom+` WHERE p.id = $1`, id)
	var pr Project
	if err := row.Scan(&pr.ID, &pr.Name, &pr.Description, &pr.CreatedBy,
		&pr.OwnerEmail, &pr.CreatedAt, &pr.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Project{}, ErrNotFound
		}
		return Project{}, err
	}
	return pr, nil
}

// CreateProject 创建项目; 重名返回 ErrConflict。
//
// createdBy 传 0 表示"没有登录用户"(测试/匿名), 用 NULLIF 落成 NULL 而不是写 0 ——
// 0 会撞外键(users 里没有 id=0), 那是把"匿名"写成了"某个不存在的用户"。
func (p *Postgres) CreateProject(
	ctx context.Context, name, description string, createdBy int64,
) (Project, error) {
	// 先插入拿 id, 再用带 JOIN 的读法把整行取回来 —— 这样**创建响应里也带 owner 邮箱**,
	// "谁建的"在任何一处都看得见(否则创建返回一个没有 owner 的对象, 前端还得再查一次)。
	var id int64
	err := p.db.QueryRowContext(ctx, `
		INSERT INTO projects (name, description, created_by)
		VALUES ($1, $2, NULLIF($3::bigint, 0))
		RETURNING id`,
		name, description, createdBy,
	).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return Project{}, ErrConflict
		}
		return Project{}, err
	}
	return p.GetProject(ctx, id)
}
