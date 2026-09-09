package store

import (
	"errors"
	"time"
)

// 通用错误, handler 层据此映射 HTTP 状态码。
var (
	ErrNotFound = errors.New("record not found")
	ErrConflict = errors.New("record conflicts with existing data")
)

// Project 对应 projects 表。
type Project struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedBy   *int64    `json:"created_by,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Corpus 对应 corpora 表。
type Corpus struct {
	ID         int64     `json:"id"`
	ProjectID  int64     `json:"project_id"`
	Name       string    `json:"name"`
	SourceType string    `json:"source_type"`
	CreatedBy  *int64    `json:"created_by,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Document 对应 documents 表。
type Document struct {
	ID        int64          `json:"id"`
	CorpusID  int64          `json:"corpus_id"`
	DocID     string         `json:"doc_id"`
	Title     string         `json:"title"`
	RawText   string         `json:"raw_text,omitempty"` // 列表查询不带该字段
	Meta      map[string]any `json:"meta,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}
