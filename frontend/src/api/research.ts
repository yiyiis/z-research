// WebSocket 客户端。
//
// 研究流程走 WebSocket（全双工长连接），不再用 SSE。
// 原因：SSE 建立在 HTTP 上，研究过程的静默期（LLM 推理几十秒）会触发各层 idle 超时断连；
// WebSocket 升级后是裸 TCP 长连接，无 HTTP idle 问题，静默期天然保持。
// 这与 gpt-researcher 的 /ws 设计一致。

// WS 推送的统一消息格式（与后端 api.wsMessage 对齐）。
export interface WSMessage {
  type: 'progress' | 'sources' | 'report_chunk' | 'done' | 'error' | 'human_feedback'
  stage?: string
  message?: string
  section_title?: string // 详细报告：当前正在写的章节
  sources?: Source[]
  report?: string // report_chunk/done 用：done 是完整报告，report_chunk 是累积到当前的报告
  report_id?: number

  // human_feedback 帧专属（多智能体 HITL）：
  // 服务端要求用户对当前研究大纲给反馈。
  title?: string
  sections?: string[]
  revision?: number
}

export interface Source {
  n: number
  url: string
  title: string
}

// ResearchOptions 提交研究请求时的可选项。
export interface ResearchOptions {
  // 'single'（确定性工作流）或 'multi'（多智能体状态图）
  // 或 'react'（ReAct Agent，LLM 自主调用工具）。
  // 空字符串 = 走服务端 ENGINE_MODE 配置。
  mode?: 'single' | 'multi' | 'react' | ''
  // 任务 ID（多智能体模式下用于检查点恢复）。
  task_id?: string
  // 多智能体模式下启用 Human-in-the-loop 大纲审核。
  // 开启后 Browser 节点跑完会立即把 initial_research
  // 摘要推给前端；Planner 完成后会阻塞等待用户对
  // 大纲的 accept/revise 回复。
  // 关闭时（默认）所有阶段自动 accept，调试用。
  hitl?: boolean
}

// HumanFeedbackPayload 客户端 → 服务端：用户对大纲的反馈。
export interface HumanFeedbackPayload {
  type: 'human_feedback_response'
  accept: boolean // true = 接受当前大纲
  notes?: string // accept=false 时填写修改意见
}

// ResearchCallbacks 定义研究过程中各类 WS 消息的回调。
export interface ResearchCallbacks {
  onProgress?: (stage: string, message: string, sectionTitle?: string) => void
  onSources?: (sources: Source[]) => void
  // 流式报告：每收到一个块就回调，report 是累积到当前的完整报告（可实时渲染）。
  onReportChunk?: (report: string) => void
  onDone?: (report: string, sources: Source[], reportId: number) => void
  onError?: (message: string) => void

  // 多智能体专属：服务端发出 human_feedback 帧时触发。
  // 调用方应在 UI 上展示大纲（title+sections）并让用户接受或修改。
  // 用户回复后，调用 onHumanFeedbackReply(accept, notes) 发送。
  onHumanFeedback?: (
    title: string,
    sections: string[],
    revision: number,
    onHumanFeedbackReply: (accept: boolean, notes?: string) => void,
  ) => void
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
//
// opts.mode: 'single' 或 'multi'。多智能体模式下，
// cb.onHumanFeedback 会被服务端触发的 human_feedback
// 帧调用；调用方应在 UI 上让用户决定 accept/revise 后
// 通过回调返回的 reply 函数回发响应。
export async function runResearch(
  query: string,
  mode: 'single' | 'multi' | '' = '',
  hitl: boolean = false,
  reportType: 'brief' | 'detailed' = 'brief',
  cb: ResearchCallbacks,
): Promise<void> {
  return new Promise<void>((resolve) => {
    const ws = new WebSocket(wsURL())

    ws.onopen = () => {
      // 连接建立后发送研究请求。
      ws.send(JSON.stringify({
        query,
        mode: mode ?? '',
        hitl,
        report_type: reportType,
      }))
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
          cb.onProgress?.(m.stage ?? '', m.message ?? '', m.section_title)
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
        case 'human_feedback':
          // 多智能体专属：要求用户对当前大纲给反馈。
          // 包装一个 reply 函数给调用方，回发时直接 write。
          cb.onHumanFeedback?.(
            m.title ?? '',
            m.sections ?? [],
            m.revision ?? 0,
            (accept, notes) => {
              const payload: HumanFeedbackPayload = {
                type: 'human_feedback_response',
                accept,
                notes: notes ?? '',
              }
              if (ws.readyState === WebSocket.OPEN) {
                ws.send(JSON.stringify(payload))
              }
            },
          )
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
