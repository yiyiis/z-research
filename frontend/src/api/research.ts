// WebSocket 客户端。
//
// 研究流程走 WebSocket（全双工长连接），不再用 SSE。
// 原因：SSE 建立在 HTTP 上，研究过程的静默期（LLM 推理几十秒）会触发各层 idle 超时断连；
// WebSocket 升级后是裸 TCP 长连接，无 HTTP idle 问题，静默期天然保持。
// 这与 gpt-researcher 的 /ws 设计一致。

// WS 推送的统一消息格式（与后端 api.wsMessage 对齐）。
export interface WSMessage {
  type: 'progress' | 'sources' | 'report_chunk' | 'done' | 'error'
  stage?: string
  message?: string
  sources?: Source[]
  report?: string // report_chunk/done 用：done 是完整报告，report_chunk 是累积到当前的报告
  report_id?: number
}

export interface Source {
  n: number
  url: string
  title: string
}

// ResearchCallbacks 定义研究过程中各类 WS 消息的回调。
export interface ResearchCallbacks {
  onProgress?: (stage: string, message: string) => void
  onSources?: (sources: Source[]) => void
  // 流式报告：每收到一个块就回调，report 是累积到当前的完整报告（可实时渲染）。
  onReportChunk?: (report: string) => void
  onDone?: (report: string, sources: Source[], reportId: number) => void
  onError?: (message: string) => void
}

// 计算 WebSocket URL：与当前页面同源，自动处理 http/https → ws/wss。
// 开发时走 Vite（5173），由 vite.config.ts 的 proxy 转发 /ws 到后端 8080。
// 生产时与页面同源（前端由后端托管）。
function wsURL(): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${window.location.host}/ws`
}

// runResearch 建立 WebSocket 连接，发送查询，逐帧回调。
// 返回时表示流结束（done 或 error 或连接关闭）。
export async function runResearch(
  query: string,
  cb: ResearchCallbacks,
): Promise<void> {
  return new Promise<void>((resolve) => {
    const ws = new WebSocket(wsURL())

    ws.onopen = () => {
      // 连接建立后发送研究请求。
      ws.send(JSON.stringify({ query }))
    }

    ws.onmessage = (ev) => {
      let m: WSMessage
      try {
        m = JSON.parse(ev.data)
      } catch {
        return // 忽略无法解析的帧（如 pong）。
      }
      switch (m.type) {
        case 'progress':
          cb.onProgress?.(m.stage ?? '', m.message ?? '')
          break
        case 'sources':
          cb.onSources?.(m.sources ?? [])
          break
        case 'report_chunk':
          cb.onReportChunk?.(m.report ?? '')
          break
        case 'done':
          cb.onDone?.(m.report ?? '', m.sources ?? [], m.report_id ?? 0)
          ws.close()
          resolve()
          break
        case 'error':
          cb.onError?.(m.message ?? '未知错误')
          ws.close()
          resolve()
          break
      }
    }

    ws.onerror = () => {
      cb.onError?.('WebSocket 连接错误')
      resolve()
    }

    ws.onclose = () => {
      // 连接关闭即结束（done/error 已自行 resolve，这里幂等）。
      resolve()
    }
  })
}

// ---- 报告 CRUD（仍走普通 REST）----

export interface ReportListItem {
  id: number
  query: string
  title: string
  sources: Source[]
  created_at: string
}

export interface ReportDetail {
  id: number
  query: string
  title: string
  content: string
  sources: Source[]
  created_at: string
}

const API_BASE = '' // 同源；开发时由 vite proxy 转发 /api 到 :8080

export async function listReports(limit = 50): Promise<ReportListItem[]> {
  const resp = await fetch(`${API_BASE}/api/reports?limit=${limit}`)
  if (!resp.ok) throw new Error(`列表失败: ${resp.status}`)
  const body = await resp.json()
  return body.items ?? []
}

export async function getReport(id: number): Promise<ReportDetail> {
  const resp = await fetch(`${API_BASE}/api/reports/${id}`)
  if (!resp.ok) throw new Error(`获取失败: ${resp.status}`)
  return resp.json()
}

export async function deleteReport(id: number): Promise<void> {
  const resp = await fetch(`${API_BASE}/api/reports/${id}`, { method: 'DELETE' })
  if (!resp.ok) throw new Error(`删除失败: ${resp.status}`)
}
