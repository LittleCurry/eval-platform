package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
)

// ListCases 列出数据集下的评测用例(按 id 升序)。
func (p *Postgres) ListCases(ctx context.Context, datasetID int64) ([]Case, error) {
	ok, err := p.datasetExists(ctx, datasetID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}

	rows, err := p.db.QueryContext(ctx, `
		SELECT id, dataset_id, qid, question, gold_anchors::text,
		       reference_answer, category, difficulty, notes, created_at, updated_at
		FROM cases WHERE dataset_id = $1 ORDER BY id`, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Case, 0)
	for rows.Next() {
		c, err := scanCase(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetCase 取单条用例。
func (p *Postgres) GetCase(ctx context.Context, id int64) (Case, error) {
	row := p.db.QueryRowContext(ctx, `
		SELECT id, dataset_id, qid, question, gold_anchors::text,
		       reference_answer, category, difficulty, notes, created_at, updated_at
		FROM cases WHERE id = $1`, id)
	c, err := scanCase(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return Case{}, ErrNotFound
	}
	if err != nil {
		return Case{}, err
	}
	return c, nil
}

// DeleteCase 删除单条用例。
func (p *Postgres) DeleteCase(ctx context.Context, id int64) error {
	res, err := p.db.ExecContext(ctx, `DELETE FROM cases WHERE id = $1`, id)
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

// ImportValidCases 对已通过 handler 纯格式校验的行做"查库类"校验并入库:
//   - 数据集必须存在(否则整体 ErrNotFound)
//   - qid 与库内已有 / 文件内已出现的重复 → 该行报错
//   - gold_anchors[].doc 必须在该数据集所属项目的语料中存在 → 该行报错
//
// 通过的行在单个事务内插入(原子), 坏行只进报告不影响好行。
func (p *Postgres) ImportValidCases(ctx context.Context, datasetID int64, inputs []CaseInput) (CaseImportResult, error) {
	var res CaseImportResult
	if len(inputs) == 0 {
		return res, nil
	}

	var projectID int64
	err := p.db.QueryRowContext(ctx,
		`SELECT project_id FROM datasets WHERE id = $1`, datasetID).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return res, ErrNotFound
	}
	if err != nil {
		return res, err
	}

	existingQIDs, err := p.caseQIDs(ctx, datasetID)
	if err != nil {
		return res, err
	}
	docSet, err := p.projectDocIDs(ctx, projectID)
	if err != nil {
		return res, err
	}

	valid := make([]CaseInput, 0, len(inputs))
	seenInFile := make(map[string]struct{}, len(inputs))
	for _, in := range inputs {
		if _, dup := existingQIDs[in.QID]; dup {
			res.Errors = append(res.Errors, CaseLineError{Line: in.Line, QID: in.QID,
				Reason: "qid 在库中已存在"})
			continue
		}
		if _, dup := seenInFile[in.QID]; dup {
			res.Errors = append(res.Errors, CaseLineError{Line: in.Line, QID: in.QID,
				Reason: "qid 在文件内重复"})
			continue
		}
		if missing := missingAnchorDoc(in.GoldAnchors, docSet); missing != "" {
			res.Errors = append(res.Errors, CaseLineError{Line: in.Line, QID: in.QID,
				Reason: "gold_anchors 引用的 doc 不存在: " + missing})
			continue
		}
		seenInFile[in.QID] = struct{}{}
		valid = append(valid, in)
	}

	if len(valid) == 0 {
		return res, nil
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback() //nolint:errcheck

	for _, in := range valid {
		anchorsJSON := []byte("[]")
		if in.GoldAnchors != nil {
			if b, err := json.Marshal(in.GoldAnchors); err == nil {
				anchorsJSON = b
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO cases (dataset_id, qid, question, gold_anchors,
			                   reference_answer, category, difficulty, notes)
			VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8)`,
			datasetID, in.QID, in.Question, string(anchorsJSON),
			nullableString(in.ReferenceAnswer), nullableString(in.Category),
			nullableString(in.Difficulty), nullableString(in.Notes)); err != nil {
			if isUniqueViolation(err) {
				res.Errors = append(res.Errors, CaseLineError{Line: in.Line, QID: in.QID,
					Reason: "qid 在库中已存在(并发插入冲突)"})
				continue
			}
			return res, err
		}
		res.Imported++
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}

	sort.Slice(res.Errors, func(i, j int) bool { return res.Errors[i].Line < res.Errors[j].Line })
	return res, nil
}

type scanFunc func(dest ...any) error

// scanCase 把一行 cases 记录扫成 Case(统一处理 NULL 文本列与 jsonb 列)。
func scanCase(scan scanFunc) (Case, error) {
	var c Case
	var anchors string
	var refAns, category, difficulty, notes sql.NullString
	err := scan(&c.ID, &c.DatasetID, &c.QID, &c.Question, &anchors,
		&refAns, &category, &difficulty, &notes, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return Case{}, err
	}
	if anchors != "" {
		_ = json.Unmarshal([]byte(anchors), &c.GoldAnchors)
	}
	c.ReferenceAnswer = refAns.String
	c.Category = category.String
	c.Difficulty = difficulty.String
	c.Notes = notes.String
	return c, nil
}

// caseQIDs 返回数据集下已有 qid 集合。
func (p *Postgres) caseQIDs(ctx context.Context, datasetID int64) (map[string]struct{}, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT qid FROM cases WHERE dataset_id = $1`, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]struct{})
	for rows.Next() {
		var qid string
		if err := rows.Scan(&qid); err != nil {
			return nil, err
		}
		out[qid] = struct{}{}
	}
	return out, rows.Err()
}

// projectDocIDs 返回某项目下全部语料的 doc_id 集合(gold 锚点校验用)。
func (p *Postgres) projectDocIDs(ctx context.Context, projectID int64) (map[string]struct{}, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT d.doc_id
		FROM documents d
		JOIN corpora c ON c.id = d.corpus_id
		WHERE c.project_id = $1`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]struct{})
	for rows.Next() {
		var docID string
		if err := rows.Scan(&docID); err != nil {
			return nil, err
		}
		out[docID] = struct{}{}
	}
	return out, rows.Err()
}

// missingAnchorDoc 返回第一个未在 docSet 中的锚点 doc; 全部存在则返回空串。
func missingAnchorDoc(anchors []Anchor, docSet map[string]struct{}) string {
	for _, a := range anchors {
		if _, ok := docSet[a.Doc]; !ok {
			return a.Doc
		}
	}
	return ""
}

// nullableString 空串转 NULL(避免 CHECK 约束被 ” 触发, 且语义上是"未填")。
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
