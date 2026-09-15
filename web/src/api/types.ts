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