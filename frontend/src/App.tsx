import { useCallback, useEffect, useState } from 'react'
import { QueryInput } from './components/QueryInput'
import { ProgressLog } from './components/ProgressLog'
import { ReportView } from './components/ReportView'
import { SourceList } from './components/SourceList'
import { HistoryPanel, getReport } from './components/HistoryPanel'
import { useResearch } from './hooks/useResearch'
import type { ReportDetail } from './api/research'

// App 主布局：左侧历史报告列表 + 右侧研究工作区。
export function App() {
  const { state, start, cancel } = useResearch()
  const [historyKey, setHistoryKey] = useState(0) // 触发历史列表刷新
  const [selectedId, setSelectedId] = useState<number | null>(null)
  const [viewing, setViewing] = useState<ReportDetail | null>(null)

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

  // 开始新研究时清空查看态。
  const handleStart = useCallback(
    (q: string) => {
      setViewing(null)
      setSelectedId(null)
      start(q)
    },
    [start],
  )

  // 决定右侧展示的报告内容：正在查看历史 → 历史；否则 → 当前研究产出。
  const showReport = viewing?.content ?? state.report
  const showSources = viewing?.sources ?? state.sources

  return (
    <div className="flex h-screen w-screen overflow-hidden bg-white">
      {/* 左侧：历史 */}
      <aside className="w-72 shrink-0 border-r border-gray-200">
        <HistoryPanel refreshKey={historyKey} onSelect={onSelectHistory} selectedId={selectedId} />
      </aside>

      {/* 右侧：工作区 */}
      <main className="flex flex-1 flex-col overflow-hidden">
        <header className="border-b border-gray-200 px-6 py-4">
          <h1 className="mb-3 text-lg font-semibold text-gray-900">z-research · AI 研究 Agent</h1>
          <QueryInput onSubmit={handleStart} onCancel={cancel} running={state.status === 'running'} />
        </header>

        <div className="flex-1 space-y-4 overflow-y-auto p-6">
          {state.status === 'error' && (
            <div className="rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-600">
              ❌ {state.error}
            </div>
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
              输入一个问题，开始 AI 研究
            </div>
          )}
        </div>
      </main>
    </div>
  )
}
