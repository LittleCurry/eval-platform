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

export interface Run {
    id: number
    project_id: number
    dataset_id: number
    corpus_id?: number
    status: string
    config_hash: string
    git_sha: string
    metrics: MetricsMap
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

export interface RunCaseResult {
    case_id: number
    qid: string
    question: string
    difficulty?: string
    category?: string
    retrieved: RetrievedItem[]
    metrics: MetricsMap
    flags: string[]
    latency_ms?: number
}

export interface RunReport {
    run: Run
    metrics: MetricsMap
    worst_cases: RunCaseResult[]
    flag_counts: Record<string, number>
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