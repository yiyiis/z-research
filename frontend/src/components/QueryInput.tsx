import { useState } from 'react'

interface Props {
  onSubmit: (query: string) => void
  onCancel: () => void
  running: boolean
}

// QueryInput 查询输入框 + 提交/取消按钮。
export function QueryInput({ onSubmit, onCancel, running }: Props) {
  const [query, setQuery] = useState('')

  const submit = () => {
    if (!query.trim() || running) return
    onSubmit(query)
  }

  return (
    <div className="flex gap-2">
      <input
        type="text"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        onKeyDown={(e) => e.key === 'Enter' && submit()}
        placeholder="输入要研究的问题，例如：2026 年固态电池降本的最新进展"
        className="flex-1 rounded-lg border border-gray-300 px-4 py-2 text-sm focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-500"
        disabled={running}
      />
      {running ? (
        <button
          onClick={onCancel}
          className="rounded-lg bg-gray-500 px-4 py-2 text-sm font-medium text-white hover:bg-gray-600"
        >
          取消
        </button>
      ) : (
        <button
          onClick={submit}
          disabled={!query.trim()}
          className="rounded-lg bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700 disabled:cursor-not-allowed disabled:bg-gray-300"
        >
          开始研究
        </button>
      )}
    </div>
  )
}
