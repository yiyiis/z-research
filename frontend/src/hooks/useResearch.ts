import { useCallback, useRef, useState } from 'react'
import { runResearch } from '../api/research'
import type { Source } from '../api/research'

// ResearchState 描述研究流程的状态机。
export type ResearchStatus = 'idle' | 'running' | 'done' | 'error'

export interface ProgressItem {
  stage: string
  message: string
}

export interface ResearchState {
  status: ResearchStatus
  progresses: ProgressItem[] // 累积的进度项（用于进度日志展示）
  sources: Source[] // 当前来源列表
  report: string // 最终报告 Markdown
  reportId: number | null // 已保存的报告 ID
  error: string | null
}

const initialState: ResearchState = {
  status: 'idle',
  progresses: [],
  sources: [],
  report: '',
  reportId: null,
  error: null,
}

// useResearch 封装"提交查询 → 流式接收进度 → 完成展示报告"的完整状态机。
// 底层走 WebSocket（见 api/research.ts）。
export function useResearch() {
  const [state, setState] = useState<ResearchState>(initialState)
  const wsRef = useRef<WebSocket | null>(null)

  const start = useCallback(async (query: string) => {
    const trimmed = query.trim()
    if (!trimmed) return

    // 关闭上一次连接（如有）。
    wsRef.current?.close()

    setState({ ...initialState, status: 'running' })

    await runResearch(trimmed, {
      onProgress: (stage, message) => {
        setState((s) => ({ ...s, progresses: [...s.progresses, { stage, message }] }))
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
        }))
      },
      onError: (message) => {
        setState((s) => ({ ...s, status: 'error', error: message }))
      },
    })
  }, [])

  const cancel = useCallback(() => {
    wsRef.current?.close()
    setState((s) => ({ ...s, status: 'idle' }))
  }, [])

  const reset = useCallback(() => {
    wsRef.current?.close()
    setState(initialState)
  }, [])

  return { state, start, cancel, reset }
}
