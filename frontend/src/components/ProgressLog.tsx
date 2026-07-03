import { useEffect, useRef } from 'react'
import type { ProgressItem } from '../hooks/useResearch'
import { Stage } from '../types'

interface Props {
  progresses: ProgressItem[]
  running: boolean
}

// ProgressLog 展示 WebSocket 推送的进度流，自动滚动到底部。
export function ProgressLog({ progresses, running }: Props) {
  const endRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [progresses])

  if (progresses.length === 0 && !running) return null

  return (
    <div className="rounded-lg border border-gray-200 bg-gray-50 p-3">
      <div className="mb-2 flex items-center gap-2 text-xs font-medium text-gray-500">
        <span>{running ? '研究中…' : '进度日志'}</span>
        {running && <span className="inline-block h-2 w-2 animate-pulse rounded-full bg-blue-500" />}
      </div>
      <div className="max-h-64 space-y-1 overflow-y-auto font-mono text-xs text-gray-700">
        {progresses.map((p, i) => (
          <div key={i} className="flex gap-2">
            <span className="shrink-0 text-blue-500">[{stageLabel(p.stage)}]</span>
            <span className="break-all">{p.message || p.stage}</span>
          </div>
        ))}
        <div ref={endRef} />
      </div>
    </div>
  )
}

function stageLabel(stage: string): string {
  const map: Record<string, string> = {
    [Stage.Role]: '角色',
    [Stage.Planning]: '规划',
    [Stage.Searching]: '搜索',
    [Stage.Fetching]: '抓取',
    [Stage.Compressing]: '压缩',
    [Stage.Writing]: '撰写',
  }
  return map[stage] ?? stage
}
