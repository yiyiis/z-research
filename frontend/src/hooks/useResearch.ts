import { useCallback, useRef, useState } from 'react'
import { runResearch } from '../api/research'
import type { Source } from '../api/research'

// ResearchState 描述研究流程的状态机。
export type ResearchStatus = 'idle' | 'running' | 'awaiting_feedback' | 'done' | 'error'

export interface ProgressItem {
  stage: string
  message: string
  sectionTitle?: string
}

// HumanFeedbackRequest 是多智能体模式下服务端要求审核的快照。
export interface HumanFeedbackRequest {
  title: string
  sections: string[]
  revision: number // 0 = 首次审核，1 = 第一次 revise 后再次审核…
}

export interface ResearchState {
  status: ResearchStatus
  progresses: ProgressItem[] // 累积的进度项（用于进度日志展示）
  sources: Source[] // 当前来源列表
  report: string // 最终报告 Markdown
  reportId: number | null // 已保存的报告 ID
  error: string | null

  // 多智能体 HITL 状态。status='awaiting_feedback' 时
  // 这个字段非空，前端应展示面板让用户接受/修改。
  feedback: HumanFeedbackRequest | null

  // 多智能体 Browser 节点产出的资料摘要。仅在
  // multi + hitl 模式下有值；UI 上展示为"已检索到
  // 的资料"折叠区，让用户审大纲前能看到 Planner
  // 看到了什么。
  initialResearch: string | null
}

const initialState: ResearchState = {
  status: 'idle',
  progresses: [],
  sources: [],
  report: '',
  reportId: null,
  error: null,
  feedback: null,
  initialResearch: null,
}

// useResearch 封装"提交查询 → 流式接收进度 → 完成展示报告"的完整状态机。
// 底层走 WebSocket（见 api/research.ts）。
export function useResearch() {
  const [state, setState] = useState<ResearchState>(initialState)
  const wsRef = useRef<WebSocket | null>(null)
  // replyRef 存 onHumanFeedback 提供的 reply 函数，供
  // submitFeedback 在用户点击面板按钮时回发响应。
  const replyRef = useRef<((accept: boolean, notes?: string) => void) | null>(null)

  // mode: 'single' | 'multi' | 'react' | 'deep' | ''
  // hitl: 是否启用多智能体模式的大纲审核（仅 multi 模式有效）
  // deepOpts: 深度递归模式的 breadth/depth（仅 deep 模式有效）
  const start = useCallback(async (
    query: string,
    mode: 'single' | 'multi' | 'react' | 'deep' | '' = '',
    hitl: boolean = false,
    reportType: 'brief' | 'detailed' = 'brief',
    deepOpts?: { breadth?: number; depth?: number },
  ) => {
    const trimmed = query.trim()
    if (!trimmed) return

    // 关闭上一次连接（如有）。
    wsRef.current?.close()

    setState({ ...initialState, status: 'running' })

    await runResearch(trimmed, mode, hitl, reportType, {
      onProgress: (stage, message, sectionTitle) => {
        setState((s) => ({ ...s, progresses: [...s.progresses, { stage, message, sectionTitle }] }))
        // 多智能体 Browser 节点产出 initial_research
        // 摘要时，会以 stage="browser" 推过来（一次性
        // 大消息，存进 state.initialResearch 让
        // HumanFeedbackPanel 折叠区展示）。
        if (stage === 'browser' && message) {
          setState((s) => ({ ...s, initialResearch: message }))
        }
      },
      onSources: (sources) => {
        setState((s) => ({ ...s, sources }))
      },
      // 流式报告：每收到一个块就更新 report，前端实时渲染（逐字生成效果）。
      onReportChunk: (report) => {
        setState((s) => ({ ...s, report }))
      },
      onDone: (report, sources, reportId) => {
        setState((s) => ({
          ...s,
          status: 'done',
          report,
          reportId,
          sources: sources.length ? sources : s.sources,
          feedback: null,
        }))
      },
      onError: (message) => {
        setState((s) => ({ ...s, status: 'error', error: message, feedback: null }))
      },
      onHumanFeedback: (title, sections, revision, reply) => {
        // 把审核请求存进 state，让 UI 渲染面板。
        setState((s) => ({
          ...s,
          status: 'awaiting_feedback',
          feedback: { title, sections, revision },
        }))
        // 把 reply 存到 ref，供面板组件调用。
        replyRef.current = reply
      },
    }, deepOpts)
    // 连接关闭后清掉 reply ref。
    replyRef.current = null
  }, [])

  const submitFeedback = useCallback((accept: boolean, notes?: string) => {
    if (replyRef.current) {
      replyRef.current(accept, notes)
    }
    setState((s) => ({
      ...s,
      status: 'running',
      feedback: null,
    }))
  }, [])

  const cancel = useCallback(() => {
    wsRef.current?.close()
    replyRef.current = null
    setState((s) => ({ ...s, status: 'idle' }))
  }, [])

  const reset = useCallback(() => {
    wsRef.current?.close()
    replyRef.current = null
    setState(initialState)
  }, [])

  return { state, start, submitFeedback, cancel, reset }
}
