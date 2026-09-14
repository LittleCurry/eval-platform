package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Run 对应 runs 表(报告 API 的只读视图)。
type Run struct {
	ID             int64          `json:"id"`
	ProjectID      int64          `json:"project_id"`
	DatasetID      int64          `json:"dataset_id"`
	CorpusID       *int64         `json:"corpus_id,omitempty"`
	Status         string         `json:"status"`
	ConfigHash     string         `json:"config_hash"`
	GitSHA         string         `json:"git_sha"`
	Metrics        map[string]any `json:"metrics"`
	Error          string         `json:"error,omitempty"`
	StartedAt      *time.Time     `json:"started_at,omitempty"`
	FinishedAt     *time.Time     `json:"finished_at,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	ConfigSnapshot map[string]any `json:"config_snapshot,omitempty"` // 仅详情接口返回
}

// RunCaseResult 单题结果(JOIN cases 带题目信息, 供报告页明细表与下钻)。
type RunCaseResult struct {
	CaseID     int64          `json:"case_id"`
	QID        string         `json:"qid"`
	Question   string         `json:"question"`
	Difficulty string         `json:"difficulty,omitempty"`
	Category   string         `json:"category,omitempty"`
	Retrieved  []any          `json:"retrieved"`
	Metrics    map[string]any `json:"metrics"`
	Flags      []string       `json:"flags"`
	LatencyMS  *int           `json:"latency_ms,omitempty"`
	// M4: 生成侧结果。只跑检索的 run 里 Answer 为空串、Generation 为 {} -> 响应里省略。
	Answer     string         `json:"answer,omitempty"`
	Generation map[string]any `json:"generation,omitempty"`
}

const runColumns = `id, project_id, dataset_id, corpus_id, status, config_hash, git_sha,
	metrics::text, COALESCE(error, ''), started_at, finished_at, created_at`

// ListRuns 列出 run(dataset_id / project_id 传 0 表示不过滤), 按 id 倒序。
func (p *Postgres) ListRuns(ctx context.Context, datasetID, projectID int64, limit int) ([]Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := p.db.QueryContext(ctx, `
		SELECT `+runColumns+`
		FROM runs
		WHERE ($1 = 0 OR dataset_id = $1)
		  AND ($2 = 0 OR project_id = $2)
		ORDER BY id DESC
		LIMIT $3`, datasetID, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Run, 0)
	for rows.Next() {
		run, err := scanRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// GetRun 取单次 run(含全量配置快照)。
func (p *Postgres) GetRun(ctx context.Context, id int64) (Run, error) {
	var run Run
	var metrics, snapshot string
	var started, finished sql.NullTime

	err := p.db.QueryRowContext(ctx, `
		SELECT `+runColumns+`, config_snapshot::text
		FROM runs WHERE id = $1`, id,
	).Scan(&run.ID, &run.ProjectID, &run.DatasetID, &run.CorpusID, &run.Status,
		&run.ConfigHash, &run.GitSHA, &metrics, &run.Error, &started, &finished,
		&run.CreatedAt, &snapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, err
	}

	run.Metrics = parseJSONMap(metrics)
	run.ConfigSnapshot = parseJSONMap(snapshot)
	run.StartedAt = nullTimePtr(started)
	run.FinishedAt = nullTimePtr(finished)
	return run, nil
}

// ListRunCaseResults 取某次 run 的单题结果, 按 recall 升序(最差的排前面, 便于找 bad case)。
func (p *Postgres) ListRunCaseResults(
	ctx context.Context, runID int64, limit int, flaggedOnly bool,
) ([]RunCaseResult, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := p.db.QueryContext(ctx, `
		SELECT cr.case_id, c.qid, c.question,
		       COALESCE(c.difficulty, ''), COALESCE(c.category, ''),
		       cr.retrieved::text, cr.metrics::text, cr.flags::text, cr.latency_ms,
		       COALESCE(cr.answer, ''), COALESCE(cr.generation::text, '{}')
		FROM case_results cr
		JOIN cases c ON c.id = cr.case_id
		WHERE cr.run_id = $1
		  AND (NOT $2 OR jsonb_array_length(cr.flags) > 0)
		ORDER BY COALESCE((cr.metrics->>'recall')::float8, 0) ASC, cr.case_id ASC
		LIMIT $3`, runID, flaggedOnly, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]RunCaseResult, 0)
	for rows.Next() {
		var item RunCaseResult
		var retrieved, metrics, flags, generation string
		var latency sql.NullInt64

		if err := rows.Scan(&item.CaseID, &item.QID, &item.Question, &item.Difficulty,
			&item.Category, &retrieved, &metrics, &flags, &latency, &item.Answer, &generation); err != nil {
			return nil, err
		}
		item.Retrieved = parseJSONList(retrieved)
		item.Metrics = parseJSONMap(metrics)
		item.Flags = parseJSONStrings(flags)
		item.Generation = parseJSONMap(generation)
		if latency.Valid {
			value := int(latency.Int64)
			item.LatencyMS = &value
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ListRunFlagCounts 统计某次 run 各归因标签的数量(M4 起有值; 现在通常为空)。
func (p *Postgres) ListRunFlagCounts(ctx context.Context, runID int64) (map[string]int, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT flag, count(*)::int
		FROM case_results cr, jsonb_array_elements_text(cr.flags) AS flag
		WHERE cr.run_id = $1
		GROUP BY flag
		ORDER BY count(*) DESC, flag`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]int)
	for rows.Next() {
		var flag string
		var count int
		if err := rows.Scan(&flag, &count); err != nil {
			return nil, err
		}
		out[flag] = count
	}
	return out, rows.Err()
}

// scanRun 把一行 runs 记录扫成 Run(统一处理 NULL 列与 jsonb 列)。
func scanRun(scan scanFunc) (Run, error) {
	var run Run
	var metrics string
	var started, finished sql.NullTime

	if err := scan(&run.ID, &run.ProjectID, &run.DatasetID, &run.CorpusID, &run.Status,
		&run.ConfigHash, &run.GitSHA, &metrics, &run.Error, &started, &finished,
		&run.CreatedAt); err != nil {
		return Run{}, err
	}
	run.Metrics = parseJSONMap(metrics)
	run.StartedAt = nullTimePtr(started)
	run.FinishedAt = nullTimePtr(finished)
	return run, nil
}

func nullTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time
	return &t
}

func parseJSONMap(raw string) map[string]any {
	out := make(map[string]any)
	if raw == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func parseJSONList(raw string) []any {
	out := make([]any, 0)
	if raw == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func parseJSONStrings(raw string) []string {
	out := make([]string, 0)
	if raw == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}
