import { ApiError, get, seg } from './api'
import type { DuplicateGroup, ImportOptions, ImportResult, SimilarHit } from './types-library'

export const libraryApi = {
  similar: (id: string, limit = 12) => get<{ items: SimilarHit[] }>(`/assets/${seg(id)}/similar?limit=${limit}`).then((r) => r.items),
  duplicates: () => get<{ groups: DuplicateGroup[] }>('/duplicates').then((r) => r.groups),
}

export interface ImportHandle {
  promise: Promise<ImportResult>
  abort: () => void
}

/** Uploads a zip archive and imports it as a pack. The options part precedes the file. */
export function importZip(file: File, options: ImportOptions, onProgress?: (fraction: number) => void): ImportHandle {
  const form = new FormData()
  form.append('options', new Blob([JSON.stringify(options)], { type: 'application/json' }))
  form.append('file', file, file.name)
  const xhr = new XMLHttpRequest()
  const promise = new Promise<ImportResult>((resolve, reject) => {
    xhr.open('POST', '/api/v1/import')
    xhr.responseType = 'json'
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && onProgress) onProgress(e.loaded / e.total)
    }
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) resolve(xhr.response)
      else reject(new ApiError(xhr.status, xhr.response?.error?.code ?? 'error', xhr.response?.error?.message ?? 'Import failed'))
    }
    xhr.onerror = () => reject(new ApiError(0, 'network', 'Import failed: connection lost'))
    xhr.onabort = () => reject(new ApiError(0, 'aborted', 'Import cancelled'))
    xhr.send(form)
  })
  return { promise, abort: () => xhr.abort() }
}
