package http

import (
	"strings"

	"eval-platform/server/internal/eval"
)

// 提交请求(M5)与配置模板(M5-2)共用的"请求 → 校验后的配置"解析。
//
// 为什么单独放一个文件: 提交与预览必须用**同一套规则**。若各写一份, 迟早出现
// "模板预览说有 500 字切分、真提交时按 300 跑"这种最难查的偏差 —— 就像 D14 之前
// "CLI 开关决定行为、报告里却看不到"那样。
//
// 三个请求结构体本身(submitRunChunking / submitRunGeneration / submitRunJudge)留在 runs.go,
// 它们是提交 API 的一部分; 这里只放解析与校验。

// resolveChunkingRequest 解析切分配置; 不传则用与 worker 一致的默认值。
func resolveChunkingRequest(req *submitRunChunking) (eval.ChunkingConfig, error) {
	config := eval.DefaultChunking()
	if req == nil {
		return config, nil
	}
	config = eval.ChunkingConfig{
		Strategy:  req.Strategy,
		ChunkSize: req.ChunkSize,
		Overlap:   req.Overlap,
		MinChars:  req.MinChars,
	}
	if err := config.Validate(); err != nil {
		return eval.ChunkingConfig{}, err
	}
	return config, nil
}

// resolveGenerationRequest 解析生成配置; nil 表示"不启用生成"(D14: 快照不写 generation 段)。
//
// 字段用指针区分"没传"(取服务端默认)与"显式传了 0": temperature=0 是确定性采样,
// 用零值判断会把用户的显式 0 覆盖掉。
func resolveGenerationRequest(
	req *submitRunGeneration, defaults eval.GenerationConfig,
) (*eval.GenerationConfig, error) {
	if req == nil {
		return nil, nil
	}
	config := defaults
	if value := strings.TrimSpace(req.Provider); value != "" {
		config.Provider = value
	}
	if value := strings.TrimSpace(req.BaseURL); value != "" {
		config.BaseURL = value
	}
	if value := strings.TrimSpace(req.Model); value != "" {
		config.Model = value
	}
	if value := strings.TrimSpace(req.PromptID); value != "" {
		config.PromptID = value
	}
	if req.Temperature != nil {
		config.Temperature = *req.Temperature
	}
	if req.MaxTokens != nil {
		config.MaxTokens = *req.MaxTokens
	}
	if req.MaxContextChars != nil {
		config.MaxContextChars = *req.MaxContextChars
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &config, nil
}

// resolveJudgeRequest 解析判定配置; nil 表示"不启用判定"(快照不写 judge 段)。
// enable_rubric=false 与 temperature=0 都是有意义的取值, 所以同样要用指针。
func resolveJudgeRequest(req *submitRunJudge, defaults eval.JudgeConfig) (*eval.JudgeConfig, error) {
	if req == nil {
		return nil, nil
	}
	config := defaults
	if value := strings.TrimSpace(req.Provider); value != "" {
		config.Provider = value
	}
	if value := strings.TrimSpace(req.BaseURL); value != "" {
		config.BaseURL = value
	}
	if value := strings.TrimSpace(req.Model); value != "" {
		config.Model = value
	}
	if value := strings.TrimSpace(req.ClaimsPromptID); value != "" {
		config.ClaimsPromptID = value
	}
	if value := strings.TrimSpace(req.RubricPromptID); value != "" {
		config.RubricPromptID = value
	}
	if req.Temperature != nil {
		config.Temperature = *req.Temperature
	}
	if req.MaxTokens != nil {
		config.MaxTokens = *req.MaxTokens
	}
	if req.MaxContextChars != nil {
		config.MaxContextChars = *req.MaxContextChars
	}
	if req.EnableRubric != nil {
		config.EnableRubric = *req.EnableRubric
	}
	if req.MaxClaims != nil {
		config.MaxClaims = *req.MaxClaims
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &config, nil
}

// resolveTopK 校验检索条数(与提交侧同一条口径)。
func resolveTopK(value int) (int, error) {
	if value <= 0 {
		return 5, nil
	}
	if value > 50 {
		return 0, errTopKTooLarge
	}
	return value, nil
}

// errTopKTooLarge 与提交侧的文案保持一致, 免得两处报错不一样。
// (想更简洁可以换成 errors.New, 行为等价。)
var errTopKTooLarge = errorString("top_k 过大(上限 50)")

type errorString string

func (e errorString) Error() string { return string(e) }
