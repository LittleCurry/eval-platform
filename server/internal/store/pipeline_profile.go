package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// PipelineProfile 对应 pipeline_profiles 表(M5-2): 一组可复用的评测配置。
//
// Config 的形状与 POST /runs 的提交字段一一对应:
//
//	{"chunking": {"strategy": "headings", "chunk_size": 500, "overlap": 50, "min_chars": 80},
//	 "retrieval": {"top_k": 5},
//	 "generation": {"provider": "...", "model": "...", "prompt_id": "qa_zh_v1", "temperature": 0, ...},
//	 "judge": {"model": "...", "claims_prompt_id": "...", "rubric_prompt_id": "...", "enable_rubric": true}}
//
// generation / judge 为 null 表示该模板"只跑检索" —— 与 D14 的语义完全一致
// (段不存在 = 不启用), 所以模板不会凭空打开一个实验阶段。
type PipelineProfile struct {
	ID          int64          `json:"id"`
	ProjectID   int64          `json:"project_id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Config      map[string]any `json:"config"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

const pipelineProfileColumns = `id, project_id, name, description, config::text, created_at, updated_at`

// ListPipelineProfiles 按项目列出配置模板(按 id 升序)。
func (p *Postgres) ListPipelineProfiles(ctx context.Context, projectID int64) ([]PipelineProfile, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT `+pipelineProfileColumns+`
		FROM pipeline_profiles WHERE project_id = $1 ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]PipelineProfile, 0)
	for rows.Next() {
		profile, err := scanPipelineProfile(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, profile)
	}
	return out, rows.Err()
}

// GetPipelineProfile 按 id 取配置模板。
func (p *Postgres) GetPipelineProfile(ctx context.Context, id int64) (PipelineProfile, error) {
	row := p.db.QueryRowContext(ctx, `
		SELECT `+pipelineProfileColumns+`
		FROM pipeline_profiles WHERE id = $1`, id)
	profile, err := scanPipelineProfile(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return PipelineProfile{}, ErrNotFound
	}
	if err != nil {
		return PipelineProfile{}, err
	}
	return profile, nil
}

// CreatePipelineProfile 创建配置模板。
// 项目不存在 → ErrNotFound; 同项目重名 → ErrConflict。
func (p *Postgres) CreatePipelineProfile(
	ctx context.Context, projectID int64, name, description string, config map[string]any,
) (PipelineProfile, error) {
	payload, err := json.Marshal(config)
	if err != nil {
		return PipelineProfile{}, err
	}
	row := p.db.QueryRowContext(ctx, `
		INSERT INTO pipeline_profiles (project_id, name, description, config)
		VALUES ($1, $2, $3, $4::jsonb)
		RETURNING `+pipelineProfileColumns,
		projectID, name, description, string(payload))
	profile, err := scanPipelineProfile(row.Scan)
	if err != nil {
		switch {
		case isForeignKeyViolation(err):
			return PipelineProfile{}, ErrNotFound // 外键失败 = project 不存在
		case isUniqueViolation(err):
			return PipelineProfile{}, ErrConflict
		default:
			return PipelineProfile{}, err
		}
	}
	return profile, nil
}

// UpdatePipelineProfile 局部更新; 指针传 nil 表示不改该字段。
// config 传 nil 表示不改配置 —— 注意这与"把配置改成 null"是两件事, 靠指针区分。
func (p *Postgres) UpdatePipelineProfile(
	ctx context.Context, id int64, name, description *string, config map[string]any,
) (PipelineProfile, error) {
	var payload *string
	if config != nil {
		encoded, err := json.Marshal(config)
		if err != nil {
			return PipelineProfile{}, err
		}
		value := string(encoded)
		payload = &value
	}
	row := p.db.QueryRowContext(ctx, `
		UPDATE pipeline_profiles
		SET name        = COALESCE($2, name),
		    description = COALESCE($3, description),
		    config      = COALESCE($4::jsonb, config),
		    updated_at  = now()
		WHERE id = $1
		RETURNING `+pipelineProfileColumns,
		id, name, description, payload)
	profile, err := scanPipelineProfile(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return PipelineProfile{}, ErrNotFound
	}
	if err != nil {
		if isUniqueViolation(err) {
			return PipelineProfile{}, ErrConflict
		}
		return PipelineProfile{}, err
	}
	return profile, nil
}

// DeletePipelineProfile 删除配置模板。
func (p *Postgres) DeletePipelineProfile(ctx context.Context, id int64) error {
	res, err := p.db.ExecContext(ctx, `DELETE FROM pipeline_profiles WHERE id = $1`, id)
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

func scanPipelineProfile(scan func(...any) error) (PipelineProfile, error) {
	var profile PipelineProfile
	var raw string
	if err := scan(&profile.ID, &profile.ProjectID, &profile.Name, &profile.Description,
		&raw, &profile.CreatedAt, &profile.UpdatedAt); err != nil {
		return PipelineProfile{}, err
	}
	profile.Config = parseJSONMap(raw)
	return profile, nil
}
