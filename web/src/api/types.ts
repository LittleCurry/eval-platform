// 与 Go 侧 json tag(snake_case)对齐的类型定义。

export interface Project {
    id: number
    name: string
    description?: string
    created_at?: string
    updated_at?: string
}

export interface Corpus {
    id: number
    project_id: number
    name: string
    source_type: string
    created_at?: string
    updated_at?: string
}

export interface Document {
    id: number
    corpus_id: number
    doc_id: string
    title: string
    raw_text?: string
    meta?: Record<string, unknown>
    created_at?: string
    updated_at?: string
}

export interface Dataset {
    id: number
    project_id: number
    name: string
    description?: string
    case_count?: number
    created_at?: string
    updated_at?: string
}

export interface Anchor {
    doc: string
    span?: string
}

export interface CaseItem {
    id: number
    dataset_id: number
    qid: string
    question: string
    gold_anchors: Anchor[]
    reference_answer?: string
    category?: string
    difficulty?: string
    notes?: string
    created_at?: string
    updated_at?: string
}

export interface CaseImportError {
    line: number
    qid?: string
    reason: string
}

export interface ImportReport {
    total: number
    imported: number
    errors: CaseImportError[]
}

// ---- 评测运行(runs / case_results) ----

export type MetricsMap = Record<string, number>

// M4-3(D15): 归因规则版本/覆盖范围/阈值。随 run 落库在 metrics.attribution,
// 与数值指标同住一个 map —— 所以 run 级 metrics 不是纯 Record<string, number>。
export interface AttributionMeta {
    version: number
    scope: 'retrieval' | 'retrieval+judge'
    k: number
    low_rank_limit: number
    low_rank_ratio: number
    quality_line: number
}

/** run 级指标: 数值指标 + 归因元信息。 */
export type RunMetrics = MetricsMap & { attribution?: AttributionMeta }

export interface Run {
    id: number
    project_id: number
    dataset_id: number
    corpus_id?: number
    status: string
    config_hash: string
    git_sha: string
    metrics: RunMetrics
    error?: string
    started_at?: string
    finished_at?: string
    created_at: string
    config_snapshot?: Record<string, unknown>
}

export interface RetrievedItem {
    point_id: string
    doc_id: string
    score: number
}

// M4-1/M4-2: 生成与判定的落库结构(case_results.answer / generation / judge)。
// 服务端从 M4-1 起就在返回这些字段, 前端直到 M4-4 才展示它们。

export interface GenerationPayload {
    model?: string
    provider?: string
    prompt_id?: string
    temperature?: number
    max_tokens?: number
    prompt_tokens?: number
    completion_tokens?: number
    latency_ms?: number
    context_chunks?: number
    prompt_hash?: string
}

export type ClaimLabel = 'supported' | 'unsupported' | 'irrelevant'

export interface JudgeClaim {
    id: number
    text: string
    label: ClaimLabel
    evidence?: string
    reason?: string
}

export interface JudgeRubric {
    relevance: number
    helpfulness: number
    reason?: string
}

export interface JudgePayload {
    claims: JudgeClaim[]
    /** 未启用 rubric 时为 null —— 展示层必须与 "0 分" 区分开。 */
    rubric: JudgeRubric | null
    meta: Record<string, unknown>
}

export interface RunCaseResult {
    case_id: number
    qid: string
    question: string
    difficulty?: string
    category?: string
    retrieved: RetrievedItem[]
    metrics: MetricsMap
    /** 归因标签, 按优先级排序: flags[0] 即主因(process.md D16)。 */
    flags: string[]
    latency_ms?: number
    /** 只跑检索的 run 里为空, 响应中省略。 */
    answer?: string
    generation?: GenerationPayload
    judge?: JudgePayload
}

export interface RunReport {
    run: Run
    metrics: RunMetrics
    worst_cases: RunCaseResult[]
    flag_counts: Record<string, number>
}

// M4-4.1: 按需从向量库取回的 chunk 正文(不落库, 见 process.md D17)。

export interface CaseContextChunk {
    point_id: string
    doc_id?: string
    score?: number
    section?: string
    text?: string
    /** false = 向量库里查不到这个 point(集合被重建过), 展示层显示"正文缺失"。 */
    found: boolean
}

export interface CaseContextResponse {
    /** 本次取正文用的 Qdrant 集合名(corpus{id}_{切分指纹前 8 位})。 */
    collection: string
    /** 与 case_results.retrieved 同顺序。 */
    chunks: CaseContextChunk[]
    /** 非空表示这次取不到正文(向量库不可用等): 其余内容照常展示, 只有这块降级。 */
    error?: string
}

// ---- 配置模板(M5-2) ----

export interface ProfileChunking {
    strategy: string
    chunk_size: number
    overlap: number
    min_chars: number
}

export interface ProfileRetrieval {
    top_k: number
}

export interface ProfileGeneration {
    provider?: string
    base_url?: string
    model?: string
    prompt_id?: string
    temperature?: number
    max_tokens?: number
    max_context_chars?: number
}

export interface ProfileJudge {
    provider?: string
    base_url?: string
    model?: string
    claims_prompt_id?: string
    rubric_prompt_id?: string
    temperature?: number
    max_tokens?: number
    max_context_chars?: number
    enable_rubric?: boolean
    max_claims?: number
}

/** 模板配置: 与 POST /runs 的旋钮一一对应; generation/judge 为 null 表示该阶段不启用。 */
export interface PipelineProfileConfig {
    chunking: ProfileChunking
    retrieval: ProfileRetrieval
    generation: ProfileGeneration | null
    judge: ProfileJudge | null
}

export interface PipelineProfile {
    id: number
    project_id: number
    name: string
    description: string
    config: PipelineProfileConfig
    created_at: string
    updated_at: string
}

/** 预览响应: config_hash 与真提交落库的值逐字相同(见 process.md D7/D14)。 */
export interface PipelinePreview {
    snapshot: Record<string, unknown>
    config_hash: string
    chunking_hash: string
    collection: string
    generation_enabled: boolean
    judge_enabled: boolean
}

// ---- A/B 对比(M5-1, process.md D6/D18) ----

export interface ABMetricDelta {
    left: number
    right: number
    delta: number
    improved: number
    worsened: number
    unchanged: number
    p_value: number
    /** 统计显著 **且** 差值超出噪声底 —— 两个条件缺一不可。 */
    significant: boolean
    /** 差值落在跨 run 噪声底内: 展示层必须显示"无法区分", 不许说变好/变坏。 */
    below_noise: boolean
    /** 实际参与该指标对比的题数(生成侧只统计两侧都有判定的题)。 */
    cases: number
    /** 指标方向: false 表示越低越好(幻觉率/无关率), 配色要反着来。 */
    higher_is_better: boolean
}

export interface ABCaseDelta {
    qid: string
    category?: string
    difficulty?: string
    flags_left: string[]
    flags_right: string[]
}

export interface ABStratum {
    key: string
    cases: number
    mean_delta: number
    improved: number
    worsened: number
    flagged_left: number
    flagged_right: number
}

export interface ABReport {
    left: number
    right: number
    /** false 时下面的统计没有意义, reason 说明原因。 */
    comparable: boolean
    reason?: string
    left_cases: number
    right_cases: number
    shared_cases: number
    summary: Record<string, ABMetricDelta>
    noise_floor: number
    fixed: ABCaseDelta[]
    broke: ABCaseDelta[]
    changed: ABCaseDelta[]
    by_category: ABStratum[]
    by_difficulty: ABStratum[]
    by_flag: ABStratum[]
    /** 非空表示两侧归因准备度不一致, 标签层面对比不可信。 */
    attribution_missing?: string
    /** 两侧都有判定的题数(生成侧指标的配对样本量)。 */
    judged_cases: number
    /** 非空表示生成侧指标这次没得比(例如两侧都没有判定)。 */
    generation_note?: string
}

// ---- Bad Case 标注(M6) ----

export type AnnotationStatus = 'open' | 'fixed' | 'verified' | 'wontfix'

/** 人工确认的归因: 与 M4-3 的标签族对齐; '' = 还没归类。 */
export type AnnotationReason = '' | 'retrieval' | 'hallucination' | 'generation' | 'dataset' | 'unknown'

export interface Annotation {
    id: number
    project_id: number
    run_id: number
    case_id: number
    status: AnnotationStatus
    reason: AnnotationReason
    comment: string
    assignee: string
    created_by: string
    created_at: string
    updated_at: string
}

export interface AnnotationStats {
    total: number
    by_status: Record<string, number>
    by_reason: Record<string, number>
}

/** 归因建议: 由机器标签推导(flags[0] = 主因), 人工可以改成别的值。 */
export interface AnnotationSuggestion {
    reason: string
    rationale: string
    flags: string[]
}


// ---- 任务进度(jobs) ----

export interface JobProgress {
    pending: number
    running: number
    succeeded: number
    failed: number
    total: number
}

export interface Job {
    id: number
    run_id: number
    status: string
    progress: JobProgress
    heartbeat_at?: string
    error?: string
    created_at?: string
    updated_at?: string
}

export interface RunProgress {
    run_id: number
    status: string
    job: Job | null
    stale: boolean
    stale_after_seconds: number
}