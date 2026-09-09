import { http } from './client'
import type { Corpus, Document } from './types'

export function listCorpora(projectId: number): Promise<Corpus[]> {
    return http.get(`/corpora?project_id=${projectId}`)
}

export function createCorpus(payload: {
    project_id: number
    name: string
    source_type?: string
}): Promise<Corpus> {
    return http.post('/corpora', payload)
}

export function deleteCorpus(id: number): Promise<void> {
    return http.del(`/corpora/${id}`)
}

export function listDocuments(corpusId: number): Promise<Document[]> {
    return http.get(`/corpora/${corpusId}/documents`)
}

export interface DocumentUploadItem {
    doc_id: string
    title: string
    raw_text: string
    meta?: Record<string, unknown>
}

export function uploadDocuments(
    corpusId: number,
    documents: DocumentUploadItem[],
): Promise<{ inserted: number }> {
    return http.post(`/corpora/${corpusId}/documents`, { documents })
}

export function deleteDocument(id: number): Promise<void> {
    return http.del(`/documents/${id}`)
}