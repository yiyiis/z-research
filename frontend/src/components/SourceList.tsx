import type { Source } from '../api/research'

interface Props {
  sources: Source[]
}

// SourceList 展示引用来源列表（带编号，对应报告中的 [n]）。
export function SourceList({ sources }: Props) {
  if (sources.length === 0) return null
  return (
    <div className="rounded-lg border border-gray-200 p-3">
      <div className="mb-2 text-xs font-medium text-gray-500">引用来源（{sources.length}）</div>
      <ol className="space-y-1 text-xs">
        {sources.map((s) => (
          <li key={s.n} className="flex gap-2">
            <span className="shrink-0 text-gray-400">[{s.n}]</span>
            <a
              href={s.url}
              target="_blank"
              rel="noopener noreferrer"
              className="break-all text-blue-600 hover:underline"
              title={s.url}
            >
              {s.title || s.url}
            </a>
          </li>
        ))}
      </ol>
    </div>
  )
}
