package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// 角色(与 DB 的 CHECK 约束一致, M7-1)。
//
// 三个角色不是"层级"而是"能力档": viewer 只读、editor 能写、admin 还能管人和删数据。
// 判断权限用 auth.Can(role, action), 不在这儿写 if role == "admin" 的散装判断。
const (
	RoleAdmin  = "admin"
	RoleEditor = "editor"
	RoleViewer = "viewer"
)

// User 一个账号。
//
// PasswordHash 带 json:"-": 它绝不允许出现在任何响应里 —— 靠"记得删字段"是不可靠的,
// 让序列化层直接拒绝它才是可靠的。
type User struct {
	ID           int64      `json:"id"`
	Email        string     `json:"email"`
	Name         string     `json:"name"`
	Role         string     `json:"role"`
	Disabled     bool       `json:"disabled"`
	PasswordHash string     `json:"-"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

const userColumns = `id, email, name, role, disabled, password_hash, last_login_at,
	created_at, updated_at`

// CountUsers 数账号数: 用来判断"这是不是首次启动"(第一个用户自动成为 admin)。
func (p *Postgres) CountUsers(ctx context.Context) (int, error) {
	var count int
	if err := p.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// ListUsers 按 id 升序列出全部账号(内部工具, 不分页)。
func (p *Postgres) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]User, 0)
	for rows.Next() {
		item, err := scanUser(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetUser 按 id 取账号。
func (p *Postgres) GetUser(ctx context.Context, id int64) (User, error) {
	row := p.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id)
	item, err := scanUser(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return item, nil
}

// GetUserByEmail 按 email 取账号(登录用)。调用方负责把 email 归一化成小写。
func (p *Postgres) GetUserByEmail(ctx context.Context, email string) (User, error) {
	row := p.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE email = $1`, email)
	item, err := scanUser(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return item, nil
}

// CreateUser 建账号。email 重复 -> ErrConflict。
func (p *Postgres) CreateUser(
	ctx context.Context, email, name, passwordHash, role string,
) (User, error) {
	row := p.db.QueryRowContext(ctx, `
		INSERT INTO users (email, name, password_hash, role)
		VALUES ($1, $2, $3, $4)
		RETURNING `+userColumns,
		email, name, passwordHash, role)
	item, err := scanUser(row.Scan)
	if err != nil {
		if isUniqueViolation(err) {
			return User{}, ErrConflict
		}
		return User{}, err
	}
	return item, nil
}

// UpdateUser 局部更新(name/role/disabled/passwordHash 传 nil = 不改)。
//
// 与 M6 的两个表同一套指针语义: 管用户的后台页面也是"点一下改一项",
// 零值当"没传"会把没提交的字段清空 —— 这里同样不接受那种语义。
func (p *Postgres) UpdateUser(
	ctx context.Context, id int64, name, role *string, disabled *bool, passwordHash *string,
) (User, error) {
	row := p.db.QueryRowContext(ctx, `
		UPDATE users
		SET name          = COALESCE($2, name),
		    role          = COALESCE($3, role),
		    disabled      = COALESCE($4, disabled),
		    password_hash = COALESCE($5, password_hash),
		    updated_at    = now()
		WHERE id = $1
		RETURNING `+userColumns,
		id, name, role, disabled, passwordHash)
	item, err := scanUser(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return item, nil
}

// TouchUserLogin 记录最近登录时间(登录成功后调用; 失败不影响登录本身)。
func (p *Postgres) TouchUserLogin(ctx context.Context, id int64) error {
	_, err := p.db.ExecContext(ctx, `UPDATE users SET last_login_at = now() WHERE id = $1`, id)
	return err
}

// DeleteUser 删除账号。调用方必须先确认"这不是最后一个 admin"(见 http 层的保护规则)。
func (p *Postgres) DeleteUser(ctx context.Context, id int64) error {
	res, err := p.db.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, id)
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

// CountActiveAdmins 数"还能登录的 admin"个数。
//
// 为什么需要一个专门的查询: "不能删/停用最后一个 admin"这条规则必须与停用状态一起看 ——
// 剩下两个 admin 但都被停用了, 系统照样锁死, 而单看角色是 admin 是看不出来的。
func (p *Postgres) CountActiveAdmins(ctx context.Context) (int, error) {
	var count int
	err := p.db.QueryRowContext(ctx,
		`SELECT count(*) FROM users WHERE role = $1 AND NOT disabled`, RoleAdmin).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

func scanUser(scan func(...any) error) (User, error) {
	var item User
	var lastLogin sql.NullTime
	if err := scan(&item.ID, &item.Email, &item.Name, &item.Role, &item.Disabled,
		&item.PasswordHash, &lastLogin, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return User{}, err
	}
	if lastLogin.Valid {
		value := lastLogin.Time
		item.LastLoginAt = &value
	}
	return item, nil
}
