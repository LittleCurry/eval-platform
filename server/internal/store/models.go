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

// Dataset 对应 datasets 表。
type Dataset struct {
	ID          int64     `json:"id"`
	ProjectID   int64     `json:"project_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CaseCount   int64     `json:"case_count,omitempty"` // 列表/详情附带
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Anchor 对应评测集 gold_anchors 数组元素(§5 格式)。
type Anchor struct {
	Doc  string `json:"doc"`
	Span string `json:"span,omitempty"`
}

// Case 对应 cases 表。
type Case struct {
	ID              int64     `json:"id"`
	DatasetID       int64     `json:"dataset_id"`
	QID             string    `json:"qid"`
	Question        string    `json:"question"`
	GoldAnchors     []Anchor  `json:"gold_anchors"`
	ReferenceAnswer string    `json:"reference_answer,omitempty"`
	Category        string    `json:"category,omitempty"`
	Difficulty      string    `json:"difficulty,omitempty"`
	Notes           string    `json:"notes,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// CaseInput 是导入流程中已通过"纯格式校验"的行(含原文件行号)。
type CaseInput struct {
	Line            int
	QID             string
	Question        string
	GoldAnchors     []Anchor
	ReferenceAnswer string
	Category        string
	Difficulty      string
	Notes           string
}

// CaseImportResult 描述一次 jsonl 导入的结果。
type CaseImportResult struct {
	Imported int64
	Errors   []CaseLineError
}

// CaseLineError 描述单个失败行。
type CaseLineError struct {
	Line   int    `json:"line"`
	QID    string `json:"qid"`
	Reason string `json:"reason"`
}
