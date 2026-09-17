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


/**
 * 提交评测的返回(POST /runs): 落库并入队后立刻返回, 不等 worker 跑完(M3 异步化)。
 */
export interface RunJobRef {
    run_id: number
    job_id: number
    items: number
}

// ---- 认证与 RBAC(M7-1) ----

export type Role = 'admin' | 'editor' | 'viewer'

/**
 * 账号信息。
 *
 * 刻意**不含 password_hash**: 后端把它标了 json:"-" 根本不返回,
 * 前端类型也不给它留位置 —— 两处都不给, 才不会有人"顺手"展示它。
 */
export interface UserAccount {
    id: number
    email: string
    name: string
    role: Role
    disabled: boolean
    last_login_at?: string
    created_at: string
    updated_at: string
}

export interface SessionUser {
    id: number
    email: string
    name: string
    role: Role
}

// ---- 人工金标与 judge 校准(M6-3) ----

/** 三值判定: "看不清"必须能表达 —— 逼标注员二选一, κ 会被瞎猜污染。 */
export type GoldVerdict = 'faithful' | 'hallucinated' | 'unclear'

export interface HumanGoldScore {
    id: number
    project_id: number
    run_id: number
    case_id: number
    /** 谁判的。进唯一键: 同一题允许两个人各打一份(人工间一致性要用)。 */
    annotator: string
    verdict: GoldVerdict
    /** null = 这题没打分(只判幻觉是合法用法), 不是 0 分。 */
    relevance?: number | null
    helpfulness?: number | null
    note: string
    reviewed: boolean
    created_at: string
    updated_at: string
}

/** 幻觉检测的二分类校准(混淆矩阵 + 一致率 + κ)。 */
export interface GoldBinaryCalibration {
    pairs: number
    judge_positive: number
    human_positive: number
    true_positive: number
    false_positive: number
    true_negative: number
    false_negative: number
    agreement: number
    /** κ 已扣掉"瞎猜也能对"的部分: 常数分类器一致率再高, κ 也是 0。 */
    kappa: number
    /** 人工判"看不清"而没进样本的题数。 */
    excluded_unclear: number
}

/** 有序分数(1–5)的校准。 */
export interface GoldScoreCalibration {
    pairs: number
    exact_agreement: number
    within_1: number
    mae: number
    judge_mean: number
    human_mean: number
    /** judge - 人工: 正数 = judge 更宽松。 */
    bias: number
    /** 5×5, 行 = 人工, 列 = judge(下标 0 表示 1 分)。 */
    confusion: number[][]
}

export interface GoldInterAnnotator {
    annotators_a: string
    annotators_b: string
    binary_pairs: number
    binary_kappa?: number
    score_pairs: number
    score_mae?: number
    score_exact_rate?: number
}

/** 一条人机不一致的题: missed = 漏判(人工说有幻觉), false_alarm = 误报。 */
export interface CalibrationDisagreement {
    case_id: number
    qid?: string
    kind: 'missed' | 'false_alarm'
    human_verdict: GoldVerdict
    judge_unsupported: number
}

export interface JudgeCalibration {
    run_id: number
    total_cases: number
    cases_with_gold: number
    primary_annotator: string
    annotators: string[]
    binary?: GoldBinaryCalibration
    helpfulness?: GoldScoreCalibration
    relevance?: GoldScoreCalibration
    inter_annotator?: GoldInterAnnotator
    coverage: number
    gold_judged: number
    gold_unjudged: number
    /** 这些金标里有多少条经过复核(复核 = 第二个人看过并确认)。 */
    gold_reviewed: number
    /** 人机不一致的题(漏判在前): 直接去看"judge 把哪几道题判错了"。 */
    disagreements: CalibrationDisagreement[]
    /** 报告自曝的"别信我"条件(样本<20 / 单人 / 覆盖率低)。 */
    notes: string[]
}

// ---- 标注闭环(M6-4) ----

export type ClosureVerdict = 'improved' | 'stable' | 'worsened' | 'changed' | 'unverifiable'

export interface ClosureEvidence {
    metric: string
    left: number
    right: number
    delta: number
    higher_is_better: boolean
    direction: 'better' | 'worse' | 'same'
    /** false = 某一侧不可比(例如缺判定), 数值无意义, 显示 "—"。 */
    comparable: boolean
}

export interface ClosureRecord {
    /** 标注 id: 销单(PATCH /annotations/:id)要用。 */
    annotation_id: number
    case_id: number
    qid: string
    question?: string
    status: AnnotationStatus
    reason: string
    comment: string
    assignee: string
    verdict: ClosureVerdict
    flags_left: string[] | null
    flags_right: string[] | null
    evidence: ClosureEvidence[]
    /** 状态是 fixed 且这次确实变好 —— 可以推进到 verified。 */
    eligible_for_verify: boolean
    /** 不能销单的原因(没变好 / 状态还是 open / 这次 run 里没这道题)。 */
    blocked_reason?: string
}

export interface ClosureSummary {
    annotated: number
    improved: number
    stable: number
    worsened: number
    changed: number
    unverifiable: number
    by_status: Record<string, number>
    eligible_for_verify: number
    fixed_total: number
}

export interface ClosureReport {
    /** 打标注的那次 run(待办所在)。 */
    baseline: number
    /** 改完之后的新 run。 */
    candidate: number
    comparable: boolean
    reason?: string
    baseline_hash: string
    candidate_hash: string
    /** true = 两次配置指纹相同: 这是复现不是实验。 */
    same_config: boolean
    attribution_missing?: string
    summary: ClosureSummary
    records: ClosureRecord[]
    notes: string[]
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