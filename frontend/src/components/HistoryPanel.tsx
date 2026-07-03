import { useEffect, useState } from 'react'
import { deleteReport, getReport, listReports } from '../api/research'
import type { ReportListItem } from '../api/research'

interface Props {
  refreshKey: number // 父组件通过改变此值触发刷新（如新报告完成后）
  onSelect: (id: number) => void
  selectedId: number | null
}

// HistoryPanel 历史报告列表，支持查看与删除。
export function HistoryPanel({ refreshKey, onSelect, selectedId }: Props) {
  const [items, setItems] = useState<ReportListItem[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    listReports(50)
      .then((list) => {
        if (!cancelled) {
          setItems(list)
          setError(null)
        }
      })
      .catch((e) => !cancelled && setError(String(e)))
      .finally(() => !cancelled && setLoading(false))
    return () => {
      cancelled = true
    }
  }, [refreshKey])

  const handleDelete = async (id: number) => {
    if (!confirm('确定删除这篇报告？')) return
    try {
      await deleteReport(id)
      setItems((prev) => prev.filter((r) => r.id !== id))
    } catch (e) {
      alert(`删除失败: ${e}`)
    }
  }

  return (
    <div className="flex h-full flex-col">
      <div className="border-b border-gray-200 px-4 py-3 text-sm font-semibold">历史报告</div>
      <div className="flex-1 overflow-y-auto">
        {loading && <div className="p-4 text-xs text-gray-400">加载中…</div>}
        {error && <div className="p-4 text-xs text-red-500">{error}</div>}
        {!loading && items.length === 0 && (
          <div className="p-4 text-xs text-gray-400">暂无历史</div>
        )}
        {items.map((r) => (
          <div
            key={r.id}
            onClick={() => onSelect(r.id)}
            className={`group cursor-pointer border-b border-gray-100 px-4 py-3 hover:bg-gray-50 ${
              selectedId === r.id ? 'bg-blue-50' : ''
            }`}
          >
            <div className="truncate text-sm font-medium text-gray-800">{r.title || r.query}</div>
            <div className="mt-1 truncate text-xs text-gray-400">{r.created_at}</div>
            <div className="mt-1 flex items-center justify-between">
              <span className="truncate text-xs text-gray-500">{r.query}</span>
              <button
                onClick={(e) => {
                  e.stopPropagation()
                  handleDelete(r.id)
                }}
                className="ml-2 shrink-0 text-xs text-red-400 opacity-0 hover:text-red-600 group-hover:opacity-100"
              >
                删除
              </button>
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}

// 重新导出 getReport 供 App 使用（保持导入入口统一）。
export { getReport }
