package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Job 对应 jobs 表(一次 run 的执行实例)。
type Job struct {
	ID          int64          `json:"id"`
	RunID       int64          `json:"run_id"`
	Status      string         `json:"status"`
	Progress    map[string]any `json:"progress"`
	HeartbeatAt *time.Time     `json:"heartbeat_at,omitempty"`
	Error       string         `json:"error,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// JobItem 对应 job_items 表(case 级微任务, 断点续跑的粒度)。
type JobItem struct {
	ID         int64  `json:"id"`
	JobID      int64  `json:"job_id"`
	CaseID     int64  `json:"case_id"`
	Status     string `json:"status"`
	RetryCount int    `json:"retry_count"`
	LastError  string `json:"last_error,omitempty"`
}

// CaseResultRow 单题结果(与 worker 侧同名结构对齐)。
type CaseResultRow struct {
	CaseID    int64
	Retrieved []map[string]any
	Metrics   map[string]any
	Flags     []string
	LatencyMS *int
}

// CreateRunInput 提交一次评测任务所需输入。
type CreateRunInput struct {
	ProjectID      int64
	DatasetID      int64
	CorpusID       int64
	ConfigSnapshot map[string]any
	ConfigHash     string
	GitSHA         string
}

// RunJobRef 创建结果(给 API 返回)。
type RunJobRef struct {
	RunID int64 `json:"run_id"`
	JobID int64 `json:"job_id"`
	Items int64 `json:"items"`
}

// CreateRunWithJob 一个事务内创建 run + job + 该数据集全部 case 的 job_items。
// 数据集不存在 -> ErrNotFound; 用例为空 -> 报错(避免产生永远跑不完的任务)。
func (p *Postgres) CreateRunWithJob(ctx context.Context, in CreateRunInput) (RunJobRef, error) {
	var ref RunJobRef

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return ref, err
	}
	defer tx.Rollback() //nolint:errcheck // 提交成功后回滚是空操作

	var datasetExists bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM datasets WHERE id = $1)`, in.DatasetID).Scan(&datasetExists); err != nil {
		return ref, err
	}
	if !datasetExists {
		return ref, ErrNotFound
	}

	snapshotJSON, err := json.Marshal(in.ConfigSnapshot)
	if err != nil {
		return ref, err
	}

	if err := tx.QueryRowContext(ctx, `
		INSERT INTO runs (project_id, dataset_id, corpus_id, status,
		                  config_snapshot, config_hash, git_sha)
		VALUES ($1, $2, $3, 'pending', $4::jsonb, $5, $6)
		RETURNING id`,
		in.ProjectID, in.DatasetID, in.CorpusID, string(snapshotJSON), in.ConfigHash, in.GitSHA,
	).Scan(&ref.RunID); err != nil {
		if isForeignKeyViolation(err) {
			return ref, ErrNotFound
		}
		return ref, err
	}

	if err := tx.QueryRowContext(ctx, `
		INSERT INTO jobs (run_id, status, progress)
		VALUES ($1, 'pending', '{}'::jsonb)
		RETURNING id`, ref.RunID,
	).Scan(&ref.JobID); err != nil {
		return ref, err
	}

	// 数据集下所有 case 一次性生成微任务
	res, err := tx.ExecContext(ctx, `
		INSERT INTO job_items (job_id, case_id, status)
		SELECT $1, id, 'pending' FROM cases WHERE dataset_id = $2`, ref.JobID, in.DatasetID)
	if err != nil {
		return ref, err
	}
	ref.Items, err = res.RowsAffected()
	if err != nil {
		return ref, err
	}
	if ref.Items == 0 {
		return ref, errors.New("评测集没有用例, 无法提交任务")
	}

	if err := tx.Commit(); err != nil {
		return ref, err
	}
	return ref, nil
}

// ClaimJob 领取一个待执行任务; 没有可领任务时返回 (nil, nil)。
//
// 关键: SELECT ... FOR UPDATE SKIP LOCKED —— 多 worker 并发领取互不阻塞且不会领到同一条。
func (p *Postgres) ClaimJob(ctx context.Context) (*Job, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	var jobID int64
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM jobs
		WHERE status = 'pending'
		ORDER BY id
		FOR UPDATE SKIP LOCKED
		LIMIT 1`).Scan(&jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	row := tx.QueryRowContext(ctx, `
		UPDATE jobs
		SET status = 'running', heartbeat_at = now(), updated_at = now()
		WHERE id = $1
		RETURNING id, run_id, status, progress::text, heartbeat_at,
		          COALESCE(error, ''), created_at, updated_at`, jobID)
	job, err := scanJob(row.Scan)
	if err != nil {
		return nil, err
	}
	if err := markRunRunning(ctx, tx, job.RunID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &job, nil
}

// ClaimJobItems 领取该任务下最多 limit 条待执行微任务, 置为 running。
func (p *Postgres) ClaimJobItems(ctx context.Context, jobID int64, limit int) ([]JobItem, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := p.db.QueryContext(ctx, `
		UPDATE job_items
		SET status = 'running', updated_at = now()
		WHERE id IN (
			SELECT id FROM job_items
			WHERE job_id = $1 AND status = 'pending'
			ORDER BY id
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		)
		RETURNING id, job_id, case_id, status, retry_count, COALESCE(last_error, '')`,
		jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]JobItem, 0, limit)
	for rows.Next() {
		var item JobItem
		if err := rows.Scan(&item.ID, &item.JobID, &item.CaseID, &item.Status,
			&item.RetryCount, &item.LastError); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// HeartbeatJob 刷新任务心跳(worker 定期调用; 心跳超时的 running 任务可被接管)。
func (p *Postgres) HeartbeatJob(ctx context.Context, jobID int64) error {
	_, err := p.db.ExecContext(ctx,
		`UPDATE jobs SET heartbeat_at = now(), updated_at = now() WHERE id = $1`, jobID)
	return err
}

// CompleteJobItem 单条 case 完成: 写 case_results + 推进 job_items + 刷新 job.progress。
// 全部在一个事务内 -> 这就是 checkpoint: 崩在任意时刻, 已提交的 case 不会重算。
func (p *Postgres) CompleteJobItem(
	ctx context.Context, jobID, itemID, runID int64, result CaseResultRow,
) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	retrieved, err := json.Marshal(result.Retrieved)
	if err != nil {
		return err
	}
	metrics, err := json.Marshal(result.Metrics)
	if err != nil {
		return err
	}
	flags, err := json.Marshal(result.Flags)
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO case_results (run_id, case_id, retrieved, metrics, flags, latency_ms)
		VALUES ($1, $2, $3::jsonb, $4::jsonb, $5::jsonb, $6)
		ON CONFLICT (run_id, case_id) DO UPDATE
		SET retrieved  = EXCLUDED.retrieved,
		    metrics    = EXCLUDED.metrics,
		    flags      = EXCLUDED.flags,
		    latency_ms = EXCLUDED.latency_ms,
		    updated_at = now()`,
		runID, result.CaseID, string(retrieved), string(metrics), string(flags), result.LatencyMS,
	); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE job_items
		SET status = 'succeeded', last_error = NULL, updated_at = now()
		WHERE id = $1`, itemID); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, progressSQL, jobID); err != nil {
		return err
	}
	return tx.Commit()
}

// FailJobItem 标记微任务失败: 未超重试上限则回到 pending 重新入队, 否则置 failed(死信)。
// 返回是否还会重试。
func (p *Postgres) FailJobItem(
	ctx context.Context, jobID, itemID int64, errMsg string, maxRetries int,
) (bool, error) {
	if maxRetries <= 0 {
		maxRetries = 3
	}
	var status string
	err := p.db.QueryRowContext(ctx, `
		UPDATE job_items
		SET retry_count = retry_count + 1,
		    last_error  = $2,
		    status      = CASE WHEN retry_count + 1 >= $3 THEN 'failed' ELSE 'pending' END,
		    updated_at  = now()
		WHERE id = $1
		RETURNING status`, itemID, errMsg, maxRetries).Scan(&status)
	if err != nil {
		return false, err
	}
	if _, err := p.db.ExecContext(ctx, progressSQL, jobID); err != nil {
		return false, err
	}
	return status == "pending", nil
}

// FinishJob 结束任务: 同时把所属 run 置为同样的终态(jobs 表无 finished_at, 用 updated_at)。
func (p *Postgres) FinishJob(ctx context.Context, jobID int64, status string, errMsg string) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	var runID int64
	err = tx.QueryRowContext(ctx, `
		UPDATE jobs
		SET status = $2, error = NULLIF($3, ''), updated_at = now()
		WHERE id = $1
		RETURNING run_id`, jobID, status, errMsg).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE runs
		SET status = $2, error = NULLIF($3, ''), finished_at = now(), updated_at = now()
		WHERE id = $1`, runID, status, errMsg); err != nil {
		return err
	}
	return tx.Commit()
}

// JobProgress 返回任务进度统计(pending/running/succeeded/failed/total)。
func (p *Postgres) JobProgress(ctx context.Context, jobID int64) (map[string]int, error) {
	var pending, running, succeeded, failed int
	err := p.db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE status = 'pending'),
		       count(*) FILTER (WHERE status = 'running'),
		       count(*) FILTER (WHERE status = 'succeeded'),
		       count(*) FILTER (WHERE status = 'failed')
		FROM job_items WHERE job_id = $1`, jobID,
	).Scan(&pending, &running, &succeeded, &failed)
	if err != nil {
		return nil, err
	}
	return map[string]int{
		"pending": pending, "running": running, "succeeded": succeeded, "failed": failed,
		"total": pending + running + succeeded + failed,
	}, nil
}

const progressSQL = `
	UPDATE jobs
	SET progress = (
	        SELECT jsonb_build_object(
	                   'pending',   count(*) FILTER (WHERE status = 'pending'),
	                   'running',   count(*) FILTER (WHERE status = 'running'),
	                   'succeeded', count(*) FILTER (WHERE status = 'succeeded'),
	                   'failed',    count(*) FILTER (WHERE status = 'failed'),
	                   'total',     count(*))
	        FROM job_items WHERE job_id = $1),
	    heartbeat_at = now(),
	    updated_at   = now()
	WHERE id = $1`

func scanJob(scan scanFunc) (Job, error) {
	var job Job
	var progress string
	var heartbeat sql.NullTime

	if err := scan(&job.ID, &job.RunID, &job.Status, &progress, &heartbeat,
		&job.Error, &job.CreatedAt, &job.UpdatedAt); err != nil {
		return Job{}, err
	}
	job.Progress = parseJSONMap(progress)
	job.HeartbeatAt = nullTimePtr(heartbeat)
	return job, nil
}

// DeleteRun 删除一次 run(级联 jobs/job_items/case_results); 人工重跑与测试清理使用。
func (p *Postgres) DeleteRun(ctx context.Context, runID int64) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM runs WHERE id = $1`, runID)
	return err
}

// ClaimJobByID 领取指定任务(用于接管后重跑/调试); 任务不在 pending 状态时返回 nil。
func (p *Postgres) ClaimJobByID(ctx context.Context, jobID int64) (*Job, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	var found int64
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM jobs WHERE id = $1 AND status = 'pending'
		FOR UPDATE SKIP LOCKED`, jobID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	row := tx.QueryRowContext(ctx, `
		UPDATE jobs
		SET status = 'running', heartbeat_at = now(), updated_at = now()
		WHERE id = $1
		RETURNING id, run_id, status, progress::text, heartbeat_at,
		          COALESCE(error, ''), created_at, updated_at`, jobID)
	job, err := scanJob(row.Scan)
	if err != nil {
		return nil, err
	}
	if err := markRunRunning(ctx, tx, job.RunID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &job, nil
}

// markRunRunning 任务被领取 = 实验开始执行: run 置 running 并记录 started_at。
func markRunRunning(ctx context.Context, tx *sql.Tx, runID int64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE runs
		SET status = 'running',
		    started_at = COALESCE(started_at, now()),
		    updated_at = now()
		WHERE id = $1 AND status IN ('pending', 'running')`, runID)
	return err
}

// ReclaimResult 描述一次"僵尸任务接管"的结果。
type ReclaimResult struct {
	Jobs  int   `json:"jobs"`
	Items int64 `json:"items"`
}

// ReclaimStaleJobs 接管心跳超时的任务(M3 中断恢复的关键):
// 把 running 且心跳早于 olderThanSeconds 的任务, 其 running 微任务放回 pending, 任务也回到 pending,
// 由任意 worker 重新领取并从断点继续。
//
// 安全性: 仍然使用 FOR UPDATE SKIP LOCKED, 正在心跳的活跃任务不会被误接管。
func (p *Postgres) ReclaimStaleJobs(ctx context.Context, olderThanSeconds float64) (ReclaimResult, error) {
	var result ReclaimResult

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback() //nolint:errcheck

	rows, err := tx.QueryContext(ctx, `
		SELECT id FROM jobs
		WHERE status = 'running'
		  AND (heartbeat_at IS NULL
		       OR heartbeat_at < now() - make_interval(secs => $1::double precision))
		ORDER BY id
		FOR UPDATE SKIP LOCKED`, olderThanSeconds)
	if err != nil {
		return result, err
	}
	var jobIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return result, err
		}
		jobIDs = append(jobIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return result, err
	}
	if len(jobIDs) == 0 {
		return result, nil
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE job_items
		SET status = 'pending', updated_at = now()
		WHERE job_id = ANY($1) AND status = 'running'`, jobIDs)
	if err != nil {
		return result, err
	}
	result.Items, err = res.RowsAffected()
	if err != nil {
		return result, err
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE jobs
		SET status = 'pending', error = '心跳超时被接管(worker 可能被强杀)', updated_at = now()
		WHERE id = ANY($1)`, jobIDs); err != nil {
		return result, err
	}
	for _, jobID := range jobIDs {
		if _, err := tx.ExecContext(ctx, progressSQL, jobID); err != nil {
			return result, err
		}
	}
	result.Jobs = len(jobIDs)

	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}
