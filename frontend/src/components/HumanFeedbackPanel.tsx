import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'

interface Props {
  title: string
  sections: string[]
  revision: number
  // initialResearch 是 Browser 节点跑出的资料摘要
  // （可选——仅当服务端推了对应 progress 帧时存在）。
  initialResearch?: string | null
  onSubmit: (accept: boolean, notes?: string) => void
}

// HumanFeedbackPanel 是多智能体模式的 HITL 审核面板。
//
// 展示分两块：
//   1. **初始资料摘要**（initialResearch，可折叠）—
//      多智能体 Browser 节点跑出的资料来源 + 摘录。
//      让用户在审大纲前先看 Planner 看到了什么。
//   2. **Planner 大纲**（必须显示）— 用 markdown 原样
//      渲染（`# 标题` + `- 分节1` + `## 分节2`），让用户
//      看到与 Planner 输出 1:1 的结构。
//
// 用户决定：
//   - 接受：引擎继续走 researcher → writer
//   - 拒绝：填写修改意见，引擎把意见送给 Planner 重做
//     （最多 MaxPlanRevisions 次，配置项）
// 每次重规划 revision 自增。
export function HumanFeedbackPanel({
  title,
  sections,
  revision,
  initialResearch,
  onSubmit,
}: Props) {
  const [notes, setNotes] = useState('')
  const [showRevise, setShowRevise] = useState(false)
  const [showResearch, setShowResearch] = useState(false)

  // 把 sections 数组转成与 Planner 输出 1:1 的 markdown
  // 大纲（Planner 只返 sections 列表；title 由 state
  // 单独传）。这样用户能看到 gpt-researcher 风格的纯
  // 文本大纲，不需要前端重新格式化。
  const outlineMarkdown = [
    title ? `# ${title}` : '',
    '',
    '## 大纲',
    ...sections.map((s) => `- ${s}`),
  ]
    .filter((l) => l !== '' || sections.length > 0)
    .join('\n')

  return (
    <div className="space-y-3">
      {/* 初始资料摘要（可折叠） */}
      {initialResearch && (
        <div className="rounded-lg border border-purple-200 bg-purple-50">
          <button
            onClick={() => setShowResearch(!showResearch)}
            className="flex w-full items-center justify-between px-4 py-2 text-left"
          >
            <span className="flex items-center gap-2 text-sm font-semibold text-purple-900">
              🔬 Browser 阶段：已检索到的资料
              <span className="rounded bg-purple-200 px-1.5 py-0.5 text-xs font-normal text-purple-700">
                {initialResearch.length} 字
              </span>
            </span>
            <span className="text-xs text-purple-600">
              {showResearch ? '▼ 折叠' : '▶ 展开'}
            </span>
          </button>
          {showResearch && (
            <div className="border-t border-purple-200 p-4">
              <div className="prose prose-sm max-w-none rounded bg-white p-3 prose-headings:font-semibold prose-a:text-blue-600 prose-pre:bg-gray-50">
                <ReactMarkdown remarkPlugins={[remarkGfm]}>
                  {initialResearch}
                </ReactMarkdown>
              </div>
            </div>
          )}
        </div>
      )}

      {/* 大纲审核面板（必须显示） */}
      <div className="rounded-lg border-2 border-blue-300 bg-blue-50 p-4">
        <div className="mb-3 flex items-center gap-2">
          <span className="text-lg">📋</span>
          <h3 className="text-sm font-semibold text-blue-900">
            大纲审核{revision > 0 && ` · 第 ${revision + 1} 次`}
          </h3>
        </div>

        {/* Planner 产出的大纲，用 markdown 渲染 */}
        <div className="prose prose-sm mb-3 max-w-none rounded border border-blue-200 bg-white p-3 prose-headings:font-semibold prose-headings:text-blue-900 prose-a:text-blue-600">
          <ReactMarkdown remarkPlugins={[remarkGfm]}>
            {outlineMarkdown}
          </ReactMarkdown>
        </div>

        {showRevise ? (
          <div className="space-y-2">
            <textarea
              value={notes}
              onChange={(e) => setNotes(e.target.value)}
              placeholder="描述你想要的修改，例如：请增加一节'未来展望'；或者：第 2 节改为讨论 X 主题..."
              className="w-full rounded border border-blue-300 bg-white p-2 text-sm"
              rows={3}
            />
            <div className="flex gap-2">
              <button
                onClick={() => onSubmit(false, notes)}
                disabled={notes.trim() === ''}
                className="rounded bg-blue-600 px-3 py-1.5 text-sm font-medium text-white disabled:opacity-50"
              >
                提交修改
              </button>
              <button
                onClick={() => {
                  setShowRevise(false)
                  setNotes('')
                }}
                className="rounded border border-blue-300 bg-white px-3 py-1.5 text-sm text-blue-700"
              >
                取消
              </button>
            </div>
          </div>
        ) : (
          <div className="flex gap-2">
            <button
              onClick={() => onSubmit(true)}
              className="rounded bg-green-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-green-700"
            >
              ✓ 接受大纲
            </button>
            <button
              onClick={() => setShowRevise(true)}
              className="rounded border border-blue-300 bg-white px-3 py-1.5 text-sm text-blue-700"
            >
              ✎ 请求修改
            </button>
          </div>
        )}
      </div>
    </div>
  )
}
