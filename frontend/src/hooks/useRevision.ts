// useRevision 管理报告对话式修改的状态机。
//
// 与 useResearch 独立（不污染研究状态）：
// - 研究状态机 idle/running/done/error
// - 修改状态机 idle/revising/done/error + 对话历史 messages + 流式报告 currentReport
//
// 一次修改会话基于一篇报告（baseReportId），用户可多轮输入指令，
// 每轮看到流式修改后的报告，对话历史累积。
import { useCallback, useState } from 'react'
import { runRevise as runRevision, type ReviseMessage, type Source } from '../api/research'

export type RevisionStatus = 'idle' | 'revising' | 'done' | 'error'

export interface RevisionState {
  status: RevisionStatus
  // baseReportId 是被修改的原报告 ID（修改会话基于它）。
  baseReportId: number | null
  // currentReport 是流式修改中的报告（每收到 chunk 就更新）。
  currentReport: string
  // messages 是多轮对话历史（user 指令 + assistant 修改后的报告摘要）。
  messages: ReviseMessage[]
  // newSources 是补充检索新增的来源（仅 supplement 场景）。
  newSources: Source[]
  // lastAction 是最近一次修改的类型（supplement/local_edit/restyle）。
  lastAction: string
  // savedReportId 是修改后另存的新报告 ID（用于查看/分享）。
  savedReportId: number | null
  // progress 是当前修改阶段的进度日志。
  progress: { stage: string; message: string }[]
  error: string | null
}

const initialState: RevisionState = {
  status: 'idle',
  baseReportId: null,
  currentReport: '',
  messages: [],
  newSources: [],
  lastAction: '',
  savedReportId: null,
  progress: [],
  error: null,
}

export function useRevision() {
  const [state, setState] = useState<RevisionState>(initialState)

  // startRevision 基于一篇报告发起一次修改。
  // reportId: 被修改的报告 ID
  // instruction: 用户本轮的修改指令
  // currentReport: 当前报告全文（用于流式覆盖展示）
  const startRevision = useCallback(async (
    reportId: number,
    instruction: string,
    currentReport: string,
  ) => {
    if (!instruction.trim()) return

    // 记录本轮 user 指令到对话历史。
    const userMsg: ReviseMessage = { role: 'user', content: instruction }
    const history = state.baseReportId === reportId ? state.messages : []

    setState((s) => ({
      ...initialState,
      status: 'revising',
      baseReportId: reportId,
      currentReport: currentReport, // 保留原报告，等流式 chunk 覆盖
      messages: [...history, userMsg],
      progress: [],
    }))

    await runRevision(reportId, instruction, history, {
      onProgress: (stage, message) => {
        setState((s) => ({
          ...s,
          progress: [...s.progress, { stage, message }],
        }))
      },
      onSources: (sources) => {
        setState((s) => ({ ...s, newSources: sources }))
      },
      onChunk: (report) => {
        setState((s) => ({ ...s, currentReport: report }))
      },
      onDone: ({ report, reportId: savedId, action }) => {
        setState((s) => ({
          ...s,
          status: 'done',
          currentReport: report,
          savedReportId: savedId,
          lastAction: action,
          // 把修改后的报告（截断避免历史过长）加入对话历史。
          messages: [...s.messages, { role: 'assistant', content: report.slice(0, 500) + (report.length > 500 ? '…' : '') }],
        }))
      },
      onError: (message) => {
        setState((s) => ({ ...s, status: 'error', error: message }))
      },
    })
  }, [state.baseReportId, state.messages])

  const reset = useCallback(() => {
    setState(initialState)
  }, [])

  return { state, startRevision, reset }
}
