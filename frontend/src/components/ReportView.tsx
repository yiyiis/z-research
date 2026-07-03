import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'

interface Props {
  markdown: string
}

// ReportView 渲染 Markdown 报告（支持 GFM 表格/列表等）。
export function ReportView({ markdown }: Props) {
  if (!markdown) return null
  return (
    <div className="prose prose-sm max-w-none rounded-lg border border-gray-200 p-4 prose-headings:font-semibold prose-a:text-blue-600">
      <ReactMarkdown remarkPlugins={[remarkGfm]}>{markdown}</ReactMarkdown>
    </div>
  )
}
