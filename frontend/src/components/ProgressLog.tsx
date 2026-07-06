import { useEffect, useRef, useState } from 'react'
import type { ProgressItem } from '../hooks/useResearch'
import { Stage } from '../types'

interface Props {
  progresses: ProgressItem[]
  running: boolean
}

// ProgressLog 分两部分：
//   1. 顶部状态行：只显示"当前正在做什么"一句话（如"正在撰写报告…"）。
//   2. 折叠的详细过程框：完整的 fetching/compressing 日志，点开才看。
// 这样主区域干净，报告不被日志挤占；想看中间过程随时展开。
export function ProgressLog({ progresses, running }: Props) {
  const [expanded, setExpanded] = useState(false)
  const detailEndRef = useRef<HTMLDivElement>(null)

  // 自动滚动详细日志到底部（展开时）。
  useEffect(() => {
    if (expanded) {
      detailEndRef.current?.scrollIntoView({ behavior: 'smooth' })
    }
  }, [progresses, expanded])

  if (progresses.length === 0 && !running) return null

  const latest = progresses.length > 0 ? progresses[progresses.length - 1] : null
  const statusText = latest ? formatStatus(latest) : running ? '研究中…' : ''

  return (
    <div className="space-y-2">
      {/* 顶部状态行：一句话 */}
      <div className="flex items-center gap-2 rounded-lg border border-gray-200 bg-gray-50 px-4 py-2.5 text-sm">
        {running && (
          <span className="inline-block h-2 w-2 shrink-0 animate-pulse rounded-full bg-blue-500" />
        )}
        <span className="text-gray-700">{statusText}</span>
        {/* 折叠按钮：有详细日志时显示 */}
        {progresses.length > 1 && (
          <button
            onClick={() => setExpanded((v) => !v)}
            className="ml-auto shrink-0 text-xs text-blue-600 hover:underline"
          >
            {expanded ? '收起过程' : `查看过程 (${progresses.length})`}
          </button>
        )}
      </div>

      {/* 折叠的详细过程框 */}
      {expanded && (
        <div className="rounded-lg border border-gray-200 bg-white p-3">
          <div className="mb-2 text-xs font-medium text-gray-400">详细过程</div>
          <div className="max-h-72 space-y-1 overflow-y-auto font-mono text-xs text-gray-600">
            {progresses.map((p, i) => (
              <div key={i} className="flex gap-2">
                <span className="shrink-0 text-blue-400">[{stageLabel(p.stage)}]</span>
                <span className="break-all">
                  {p.sectionTitle && <span className="text-purple-600">《{p.sectionTitle}》</span>}
                  {p.message || p.stage}
                </span>
              </div>
            ))}
            <div ref={detailEndRef} />
          </div>
        </div>
      )}
    </div>
  )
}

// formatStatus 把最新进度格式化为一句话状态。
function formatStatus(p: ProgressItem): string {
  const label = stageLabel(p.stage)
  if (p.sectionTitle) return `${label}：${p.sectionTitle}`
  if (p.message) return `${label} · ${p.message}`
  return label
}

function stageLabel(stage: string): string {
  const map: Record<string, string> = {
    [Stage.Role]: '选角色',
    [Stage.Planning]: '规划',
    [Stage.Searching]: '搜索',
    [Stage.Fetching]: '抓取',
    [Stage.Compressing]: '压缩',
    [Stage.Writing]: '撰写报告',
    [Stage.Outline]: '生成大纲',
    [Stage.Section]: '撰写章节',
  }
  return map[stage] ?? stage
}
