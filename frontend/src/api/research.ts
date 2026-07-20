// WebSocket 客户端。
//
// 研究流程走 WebSocket（全双工长连接），不再用 SSE。
// 原因：SSE 建立在 HTTP 上，研究过程的静默期（LLM 推理几十秒）会触发各层 idle 超时断连；
// WebSocket 升级后是裸 TCP 长连接，无 HTTP idle 问题，静默期天然保持。
// 这与 gpt-researcher 的 /ws 设计一致。

// WS 推送的统一消息格式（与后端 api.wsMessage 对齐）。
export interface WSMessage {
  type: 'progress' | 'sources' | 'report_chunk' | 'done' | 'error' | 'human_feedback' | 'evaluation' | 'revise_progress' | 'revise_chunk' | 'revise_sources' | 'revise_done' | 'revise_error'
  stage?: string
  message?: string
  section_title?: string // 详细报告：当前正在写的章节
  sources?: Source[]
  report?: string // report_chunk/done/revise_chunk/revise_done 用
  report_id?: number
  // done 帧附带：本次研究的 token 用量（流量计费）。
  usage?: UsageSnapshot
  // evaluation 帧附带：LLM-as-Judge 评分。
  evaluation?: Evaluation

  // human_feedback 帧专属（多智能体 HITL）：
  title?: string
  sections?: string[]
  revision?: number
}

// Evaluation 是 LLM-as-Judge 的评分结果（与后端 eval.ScoreDTO 对齐）。
export interface Evaluation {
  overall: number            // 综合分 0-10
  summary: string            // 一句话总评
  dimensions: DimensionScore[] // 各维度评分（有序）
}

// DimensionScore 是单个维度的评分。
export interface DimensionScore {
  name: string    // coverage/citation/structure/objectivity/readability
  label: string   // 中文标签
  score: number   // 0-10
  note: string    // 扣分原因
}

// UsageSnapshot 是一次研究的 token 用量快照（与后端 llm.UsageSnapshot 对齐）。
export interface UsageSnapshot {
  calls: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  reasoning_tokens?: number // 思考模型的思考 token（占 completion 的一部分）
}

export interface Source {
  n: number
  url: string
  title: string
}

// ResearchOptions 提交研究请求时的可选项。
export interface ResearchOptions {
  // 'single'（确定性工作流）/ 'multi'（多智能体状态图）
  // / 'react'（ReAct Agent，LLM 自主调用工具）
  // / 'deep'（深度递归，Lambda 节点内递归）。
  // 空字符串 = 走服务端 ENGINE_MODE 配置。
  mode?: 'single' | 'multi' | 'react' | 'deep' | ''
  // 任务 ID（多智能体模式下用于检查点恢复）。
  task_id?: string
  // 多智能体模式下启用 Human-in-the-loop 大纲审核。
  // 开启后 Browser 节点跑完会立即把 initial_research
  // 摘要推给前端；Planner 完成后会阻塞等待用户对
  // 大纲的 accept/revise 回复。
  // 关闭时（默认）所有阶段自动 accept，调试用。
  hitl?: boolean
  // 深度递归模式（mode='deep'）的 per-run 参数。
  // breadth: 递归起始扇出数（默认 4，每层按 max(2, b//2) 衰减）
  // depth: 递归层数（默认 2）
  breadth?: number
  depth?: number
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
  onDone?: (report: string, sources: Source[], reportId: number, usage?: UsageSnapshot) => void
  // 评估完成时触发（done 帧之后异步到达的 evaluation 帧）。
  onEvaluation?: (evaluation: Evaluation, reportId: number) => void
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
  mode: 'single' | 'multi' | 'react' | 'deep' | '' = '',
  hitl: boolean = false,
  reportType: 'brief' | 'detailed' = 'brief',
  cb: ResearchCallbacks,
  opts?: { breadth?: number; depth?: number },
): Promise<void> {
  return new Promise<void>((resolve) => {
    const ws = new WebSocket(wsURL())

    ws.onopen = () => {
      // 连接建立后发送研究请求。
      const payload: Record<string, unknown> = {
        query,
        mode: mode ?? '',
        hitl,
        report_type: reportType,
      }
      // 深度递归模式的 per-run 参数（仅 deep 模式生效）。
      if (mode === 'deep' && opts) {
        if (typeof opts.breadth === 'number') payload.breadth = opts.breadth
        if (typeof opts.depth === 'number') payload.depth = opts.depth
      }
      ws.send(JSON.stringify(payload))
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
          cb.onDone?.(m.report ?? '', m.sources ?? [], m.report_id ?? 0, m.usage)
          // 不立即 close：evaluation 帧会在 done 之后异步到达。
          // 设置一个超时兜底（评估慢或失败时仍能关闭连接）。
          // 若 30s 内没收到 evaluation，强制关闭。
          setTimeout(() => {
            if (ws.readyState === WebSocket.OPEN) {
              ws.close()
            }
            resolve()
          }, 30000)
          break
        case 'evaluation':
          // LLM-as-Judge 评估完成（done 之后异步到达）。
          if (m.evaluation) {
            cb.onEvaluation?.(m.evaluation, m.report_id ?? 0)
          }
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
