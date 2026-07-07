import { useCallback, useEffect, useState } from 'react'
import { QueryInput } from './components/QueryInput'
import { ProgressLog } from './components/ProgressLog'
import { ReportView } from './components/ReportView'
import { SourceList } from './components/SourceList'
import { HistoryPanel, getReport } from './components/HistoryPanel'
import { HumanFeedbackPanel } from './components/HumanFeedbackPanel'
import { useResearch } from './hooks/useResearch'
import type { ReportDetail } from './api/research'

// App 主布局：左侧历史报告列表 + 右侧研究工作区。
export function App() {
  const { state, start, submitFeedback, cancel } = useResearch()
  const [historyKey, setHistoryKey] = useState(0) // 触发历史列表刷新
  const [selectedId, setSelectedId] = useState<number | null>(null)
  const [viewing, setViewing] = useState<ReportDetail | null>(null)

  // 引擎模式：'single'（确定性工作流）或 'multi'（多智能体状态图 + HITL）
  // 或 'react'（ReAct Agent，LLM 自主调用搜索/抓取工具）。
  const [mode, setMode] = useState<'single' | 'multi' | 'react'>('multi')
  // 是否启用 HITL 大纲审核（仅 multi 模式有效）。
  // 启用时 Browser 节点会推送 initial_research 摘要；
  // Planner 完成后会阻塞等用户回复。
  const [hitl, setHitl] = useState(true)

  // 报告完成后，刷新历史列表。
  useEffect(() => {
    if (state.status === 'done') setHistoryKey((k) => k + 1)
  }, [state.status])

  // 选择历史报告时加载全文。
  const onSelectHistory = useCallback(async (id: number) => {
    setSelectedId(id)
    try {
      const r = await getReport(id)
      setViewing(r)
    } catch (e) {
      alert(`加载报告失败: ${e}`)
    }
  }, [])

  // 开始新研究时清空查看态，并把当前 mode + hitl + reportType 传给引擎。
  const handleStart = useCallback(
    (q: string, reportType: 'brief' | 'detailed') => {
      setViewing(null)
      setSelectedId(null)
      start(q, mode, hitl, reportType)
    },
    [start, mode, hitl],
  )

  // 决定右侧展示的报告内容：正在查看历史 → 历史；否则 → 当前研究产出。
  const showReport = viewing?.content ?? state.report
  const showSources = viewing?.sources ?? state.sources

  const isRunning = state.status === 'running' || state.status === 'awaiting_feedback'

  return (
    <div className="flex h-screen w-screen overflow-hidden bg-white">
      {/* 左侧：历史 */}
      <aside className="w-72 shrink-0 border-r border-gray-200">
        <HistoryPanel refreshKey={historyKey} onSelect={onSelectHistory} selectedId={selectedId} />
      </aside>

      {/* 右侧：工作区 */}
      <main className="flex flex-1 flex-col overflow-hidden">
        <header className="border-b border-gray-200 px-6 py-4">
          <div className="mb-3 flex items-center justify-between">
            <h1 className="text-lg font-semibold text-gray-900">z-research · AI 研究 Agent</h1>

            {/* 引擎模式选择（多智能体 ↔ 单 Agent） */}
            <div className="flex items-center gap-1 rounded-lg border border-gray-200 bg-gray-50 p-0.5 text-xs">
              <button
                onClick={() => setMode('single')}
                disabled={isRunning}
                className={`rounded-md px-2.5 py-1 font-medium ${
                  mode === 'single'
                    ? 'bg-white text-gray-900 shadow'
                    : 'text-gray-500 hover:text-gray-700'
                } ${isRunning ? 'cursor-not-allowed opacity-50' : ''}`}
                title="单 Agent：选角色 → 检索 → 写报告（z-research v1 行为）"
              >
                单 Agent
              </button>
              <button
                onClick={() => setMode('multi')}
                disabled={isRunning}
                className={`rounded-md px-2.5 py-1 font-medium ${
                  mode === 'multi'
                    ? 'bg-white text-blue-700 shadow'
                    : 'text-gray-500 hover:text-gray-700'
                } ${isRunning ? 'cursor-not-allowed opacity-50' : ''}`}
                title="多智能体：Planner / Reviewer / Reviser / Writer 状态图（gpt-researcher 演进版）"
              >
                多智能体
              </button>
              <button
                onClick={() => setMode('react')}
                disabled={isRunning}
                className={`rounded-md px-2.5 py-1 font-medium ${
                  mode === 'react'
                    ? 'bg-white text-green-700 shadow'
                    : 'text-gray-500 hover:text-gray-700'
                } ${isRunning ? 'cursor-not-allowed opacity-50' : ''}`}
                title="ReAct Agent：LLM 自主决定调用搜索/抓取工具、何时停止（真正的自主 Agent）"
              >
                Agent (ReAct)
              </button>
            </div>
          </div>

          {/* HITL 开关（仅 multi 模式有效） */}
          {mode === 'multi' && (
            <label className="mb-3 flex items-center gap-2 text-xs text-gray-600">
              <input
                type="checkbox"
                checked={hitl}
                disabled={isRunning}
                onChange={(e) => setHitl(e.target.checked)}
                className="h-3.5 w-3.5 rounded border-gray-300 text-blue-600 disabled:opacity-50"
              />
              <span>
                <span className="font-medium text-gray-800">人工审核大纲</span>
                <span className="ml-1 text-gray-500">
                  — Planner 出大纲后阻塞等你在面板上接受/修改（gpt-researcher HITL 模式）
                </span>
              </span>
            </label>
          )}

          <QueryInput onSubmit={handleStart} onCancel={cancel} running={isRunning} />
        </header>

        <div className="flex-1 space-y-4 overflow-y-auto p-6">
          {state.status === 'error' && (
            <div className="rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-600">
              ❌ {state.error}
            </div>
          )}

          {/* HITL 审核面板：仅在多智能体 + 等待反馈时显示 */}
          {state.status === 'awaiting_feedback' && state.feedback && (
            <HumanFeedbackPanel
              title={state.feedback.title}
              sections={state.feedback.sections}
              revision={state.feedback.revision}
              initialResearch={state.initialResearch}
              onSubmit={submitFeedback}
            />
          )}

          {(state.progresses.length > 0 || state.status === 'running') && (
            <ProgressLog progresses={state.progresses} running={state.status === 'running'} />
          )}

          {showReport && (
            <>
              <ReportView markdown={showReport} />
              <SourceList sources={showSources} />
            </>
          )}

          {!showReport && state.status === 'idle' && !viewing && (
            <div className="flex h-full items-center justify-center text-sm text-gray-400">
              {mode === 'multi' ? (
                <div className="text-center">
                  <div>输入一个问题，开始多智能体研究</div>
                  <div className="mt-1 text-xs text-gray-300">
                    Planner 出大纲 → 你审核 → 分节深度检索 → Reviewer/Reviser 自校正 → Writer 汇编
                  </div>
                </div>
              ) : (
                <div>输入一个问题，开始 AI 研究</div>
              )}
            </div>
          )}
        </div>
      </main>
    </div>
  )
}
