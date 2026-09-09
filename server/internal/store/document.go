package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// ListDocuments 列出语料库下的文档(不含 raw_text, 保持列表轻量)。
func (p *Postgres) ListDocuments(ctx context.Context, corpusID int64) ([]Document, error) {
	ok, err := p.corpusExists(ctx, corpusID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}

	rows, err := p.db.QueryContext(ctx, `
		SELECT id, corpus_id, doc_id, title, meta::text, created_at, updated_at
		FROM documents WHERE corpus_id = $1 ORDER BY doc_id`, corpusID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Document, 0)
	for rows.Next() {
		var d Document
		var meta string
		if err := rows.Scan(&d.ID, &d.CorpusID, &d.DocID, &d.Title, &meta,
			&d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		if meta != "" {
			_ = json.Unmarshal([]byte(meta), &d.Meta)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDocument 取单篇文档(含 raw_text, M2 检索需要全文)。
func (p *Postgres) GetDocument(ctx context.Context, id int64) (Document, error) {
	var d Document
	var meta string
	err := p.db.QueryRowContext(ctx, `
		SELECT id, corpus_id, doc_id, title, raw_text, meta::text, created_at, updated_at
		FROM documents WHERE id = $1`, id,
	).Scan(&d.ID, &d.CorpusID, &d.DocID, &d.Title, &d.RawText, &meta,
		&d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Document{}, ErrNotFound
	}
	if err != nil {
		return Document{}, err
	}
	if meta != "" {
		_ = json.Unmarshal([]byte(meta), &d.Meta)
	}
	return d, nil
}

// CreateDocuments 在单个事务内批量插入文档, 全部成功才提交(原子性)。
func (p *Postgres) CreateDocuments(ctx context.Context, corpusID int64, docs []Document) (int64, error) {
	ok, err := p.corpusExists(ctx, corpusID)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, ErrNotFound
	}

	// 预检已存在的 doc_id, 给出确定性错误(事务内再兜底一次 23505)
	existing, err := p.existingDocIDs(ctx, corpusID)
	if err != nil {
		return 0, err
	}
	for _, d := range docs {
		if _, dup := existing[d.DocID]; dup {
			return 0, ErrConflict
		}
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // Commit 成功后 Rollback 是空操作

	for _, d := range docs {
		metaJSON := "{}"
		if d.Meta != nil {
			b, err := json.Marshal(d.Meta)
			if err != nil {
				return 0, err
			}
			metaJSON = string(b)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO documents (corpus_id, doc_id, title, raw_text, meta)
			VALUES ($1, $2, $3, $4, $5::jsonb)`,
			corpusID, d.DocID, d.Title, d.RawText, metaJSON); err != nil {
			if isUniqueViolation(err) {
				return 0, ErrConflict
			}
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(docs)), nil
}

// DeleteDocument 删除单篇文档。
func (p *Postgres) DeleteDocument(ctx context.Context, id int64) error {
	res, err := p.db.ExecContext(ctx, `DELETE FROM documents WHERE id = $1`, id)
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

// existingDocIDs 返回语料库下已有 doc_id 集合。
func (p *Postgres) existingDocIDs(ctx context.Context, corpusID int64) (map[string]struct{}, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT doc_id FROM documents WHERE corpus_id = $1`, corpusID)
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
