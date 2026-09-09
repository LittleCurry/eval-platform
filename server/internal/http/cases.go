package http

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"eval-platform/server/internal/store"
)

// allowedDifficulties 与 DB CHECK 保持一致(双保险)。
var allowedDifficulties = map[string]bool{
	"易": true,
	"中": true,
	"难": true,
}

type caseHandler struct {
	store CaseStore
}

func newCaseHandler(s CaseStore) *caseHandler { return &caseHandler{store: s} }

// Import 导入 jsonl: body 为整个 JSONL 文本(每行一个 JSON 对象)。
// 逐行解析: 纯格式校验在 handler 层, 查库校验与入库在 store 层; 部分成功并返回报告。
func (h *caseHandler) Import(c *gin.Context) {
	datasetID, ok := parseID(c)
	if !ok {
		return
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		writeErr(c, http.StatusBadRequest, "读取请求体失败")
		return
	}
	if len(bytes.TrimSpace(body)) == 0 {
		writeErr(c, http.StatusBadRequest, "body 不能为空(JSONL 文本)")
		return
	}

	inputs := make([]store.CaseInput, 0)
	parseErrs := make([]store.CaseLineError, 0)

	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024) // 单行上限 2MB, 防超大行
	fileLine := 0
	for scanner.Scan() {
		fileLine++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue // 空行跳过, 行号仍按原文件计
		}
		in, reason := parseCaseLine(fileLine, raw)
		if reason != "" {
			parseErrs = append(parseErrs, store.CaseLineError{Line: fileLine, Reason: reason})
			continue
		}
		inputs = append(inputs, in)
	}
	if err := scanner.Err(); err != nil {
		log.Printf("scan import body: %v", err)
		writeErr(c, http.StatusInternalServerError, "读取请求体失败")
		return
	}

	res, err := h.store.ImportValidCases(c.Request.Context(), datasetID, inputs)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "数据集不存在")
			return
		}
		log.Printf("import cases: %v", err)
		writeErr(c, http.StatusInternalServerError, "导入失败")
		return
	}

	allErrs := append(parseErrs, res.Errors...)
	sort.Slice(allErrs, func(i, j int) bool { return allErrs[i].Line < allErrs[j].Line })
	writeJSON(c, http.StatusOK, gin.H{
		"total":    res.Imported + int64(len(allErrs)),
		"imported": res.Imported,
		"errors":   allErrs,
	})
}

// parseCaseLine 解析单行 JSONL 并做纯格式校验; 失败时返回原因(空串=通过)。
func parseCaseLine(line int, raw []byte) (store.CaseInput, string) {
	var l rawCaseLine
	if err := json.Unmarshal(raw, &l); err != nil {
		return store.CaseInput{}, "不是合法 JSON 对象"
	}
	l.QID = strings.TrimSpace(l.QID)
	l.Question = strings.TrimSpace(l.Question)

	in := store.CaseInput{
		Line:            line,
		QID:             l.QID,
		Question:        l.Question,
		ReferenceAnswer: l.ReferenceAnswer,
		Category:        l.Category,
		Difficulty:      l.Difficulty,
		Notes:           l.Notes,
	}
	switch {
	case in.QID == "":
		return store.CaseInput{}, "qid 必填"
	case in.Question == "":
		return store.CaseInput{}, "question 必填"
	case in.Difficulty != "" && !allowedDifficulties[in.Difficulty]:
		return store.CaseInput{}, "difficulty 必须是 易/中/难"
	}

	if len(l.GoldAnchors) > 0 {
		var anchors []store.Anchor
		if err := json.Unmarshal(l.GoldAnchors, &anchors); err != nil {
			return store.CaseInput{}, "gold_anchors 必须是数组"
		}
		for i, a := range anchors {
			if strings.TrimSpace(a.Doc) == "" {
				return store.CaseInput{}, "gold_anchors[" + strconv.Itoa(i) + "].doc 必填"
			}
		}
		in.GoldAnchors = anchors
	}
	return in, ""
}

// rawCaseLine 是 jsonl 一行的宽松映射(gold_anchors 用 RawMessage 单独校验)。
type rawCaseLine struct {
	QID             string          `json:"qid"`
	Question        string          `json:"question"`
	GoldAnchors     json.RawMessage `json:"gold_anchors"`
	ReferenceAnswer string          `json:"reference_answer"`
	Category        string          `json:"category"`
	Difficulty      string          `json:"difficulty"`
	Notes           string          `json:"notes"`
}

func (h *caseHandler) List(c *gin.Context) {
	datasetID, ok := parseID(c)
	if !ok {
		return
	}
	items, err := h.store.ListCases(c.Request.Context(), datasetID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "数据集不存在")
			return
		}
		log.Printf("list cases: %v", err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	writeJSON(c, http.StatusOK, items)
}

func (h *caseHandler) Get(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	item, err := h.store.GetCase(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "用例不存在")
			return
		}
		log.Printf("get case: %v", err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	writeJSON(c, http.StatusOK, item)
}

func (h *caseHandler) Delete(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := h.store.DeleteCase(c.Request.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "用例不存在")
			return
		}
		log.Printf("delete case: %v", err)
		writeErr(c, http.StatusInternalServerError, "删除失败")
		return
	}
	c.Status(http.StatusNoContent)
}
