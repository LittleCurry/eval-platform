import { http } from './client'
import type { CaseItem, Dataset, ImportReport } from './types'

export function listDatasets(projectId: number): Promise<Dataset[]> {
    return http.get(`/datasets?project_id=${projectId}`)
}

export function createDataset(payload: {
    project_id: number
    name: string
    description?: string
}): Promise<Dataset> {
    return http.post('/datasets', payload)
}

export function getDataset(id: number): Promise<Dataset> {
    return http.get(`/datasets/${id}`)
}

export function deleteDataset(id: number): Promise<void> {
    return http.del(`/datasets/${id}`)
}

export function listCases(datasetId: number): Promise<CaseItem[]> {
    return http.get(`/datasets/${datasetId}/cases`)
}

export function getCase(id: number): Promise<CaseItem> {
    return http.get(`/cases/${id}`)
}

export function deleteCase(id: number): Promise<void> {
    return http.del(`/cases/${id}`)
}

export function importCases(datasetId: number, jsonl: string): Promise<ImportReport> {
    return http.postRaw(`/datasets/${datasetId}/cases/import`, jsonl, 'application/x-ndjson')
}