// ReviseInput 是报告修改的输入区。
//
// 包含：修改指令输入框 + 对话历史展示（折叠）+ 当前进度日志。
// 仿 QueryInput 的交互模式（Enter 提交、revising 时禁用），
// 但去掉 reportType，加上多轮对话历史展示。
import { useState } from 'react'
import type { ReviseMessage } from '../api/research'

interface Props {
  onSubmit: (instruction: string) => void
  running: boolean
  messages: ReviseMessage[]
  progress: { stage: string; message: string }[]
  lastAction: string
}

// actionLabel 把后端的 action 代码转成中文说明。
function actionLabel(action: string): string {
  switch (action) {
    case 'supplement': return '补充检索'
    case 'local_edit': return '局部修改'
    case 'restyle': return '翻译/改风格'
    default: return ''
  }
}

export function ReviseInput({ onSubmit, running, messages, progress, lastAction }: Props) {
  const [instruction, setInstruction] = useState('')

  const submit = () => {
    const trimmed = instruction.trim()
    if (!trimmed || running) return
    onSubmit(trimmed)
    setInstruction('')
  }

  return (
    <div className="rounded-lg border border-purple-200 bg-purple-50/30 p-3">
      <div className="mb-2 flex items-center gap-2 text-xs">
        <span className="font-medium text-purple-700">✏️ 对话式修改</span>
        <span className="text-gray-400">告诉 AI 怎么改这份报告（支持多轮迭代、补充检索、翻译改风格）</span>
      </div>

      {/* 修改中进度 */}
      {running && progress.length > 0 && (
        <div className="mb-2 max-h-24 overflow-y-auto rounded bg-white/60 p-2 text-xs text-gray-600">
          {progress.slice(-3).map((p, i) => (
            <div key={i}>
              <span className="text-purple-500">[{p.stage}]</span> {p.message}
            </div>
          ))}
        </div>
      )}

      {/* 完成后显示动作类型 */}
      {!running && lastAction && (
        <div className="mb-2 text-xs text-gray-500">
          上次修改类型：<span className="font-medium text-purple-700">{actionLabel(lastAction)}</span>
        </div>
      )}

      {/* 输入框 */}
      <div className="flex gap-2">
        <input
          type="text"
          value={instruction}
          onChange={(e) => setInstruction(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault()
              submit()
            }
          }}
          placeholder={running ? '修改中…' : '如：把结论改简洁 / 补充最新的 MoE 技术 / 翻译成英文'}
          disabled={running}
          className="flex-1 rounded-md border border-gray-300 px-3 py-1.5 text-sm disabled:bg-gray-100"
        />
        <button
          onClick={submit}
          disabled={running || !instruction.trim()}
          className="shrink-0 rounded-md bg-purple-600 px-4 py-1.5 text-sm font-medium text-white hover:bg-purple-700 disabled:cursor-not-allowed disabled:bg-gray-300"
        >
          {running ? '修改中…' : '修改'}
        </button>
      </div>

      {/* 对话历史（折叠展示，≥2 轮才显示） */}
      {messages.length >= 2 && (
        <details className="mt-2 text-xs">
          <summary className="cursor-pointer text-gray-500 hover:text-gray-700">
            对话历史（{messages.length} 轮）
          </summary>
          <div className="mt-1 space-y-1">
            {messages.map((msg, i) => (
              <div key={i} className={`rounded px-2 py-1 ${msg.role === 'user' ? 'bg-purple-100/50' : 'bg-gray-100/50'}`}>
                <span className="font-medium text-gray-600">
                  {msg.role === 'user' ? '你' : 'AI'}:
                </span>
                <span className="ml-1 text-gray-600">
                  {msg.content.length > 100 ? msg.content.slice(0, 100) + '…' : msg.content}
                </span>
              </div>
            ))}
          </div>
        </details>
      )}
    </div>
  )
}
