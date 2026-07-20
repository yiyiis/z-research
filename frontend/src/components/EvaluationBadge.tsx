// EvaluationBadge 展示 LLM-as-Judge 的评分结果。
//
// 包括：综合分（彩色徽章）+ 一句话总评 + 5 维度进度条 + 扣分原因。
// 在报告区上方展示，让用户直观看到报告质量。
import type { Evaluation } from '../api/research'

interface Props {
  evaluation: Evaluation
}

// scoreColor 按分数区间返回 Tailwind 颜色类。
// ≥8 绿（优秀）/ ≥6 黄（合格）/ <6 红（需改进）。
function scoreColor(score: number): string {
  if (score >= 8) return 'text-green-700 bg-green-100'
  if (score >= 6) return 'text-yellow-700 bg-yellow-100'
  return 'text-red-700 bg-red-100'
}

function scoreBarColor(score: number): string {
  if (score >= 8) return 'bg-green-500'
  if (score >= 6) return 'bg-yellow-500'
  return 'bg-red-500'
}

export function EvaluationBadge({ evaluation }: Props) {
  if (!evaluation) return null
  const { overall, summary, dimensions } = evaluation

  return (
    <div className="rounded-lg border border-gray-200 bg-white p-4">
      <div className="mb-3 flex items-center gap-3">
        <span className="text-sm font-medium text-gray-500">📝 质量评估</span>
        <span className={`inline-flex items-center rounded-full px-3 py-1 text-sm font-bold ${scoreColor(overall)}`}>
          {overall.toFixed(1)} / 10
        </span>
        <span className="flex-1 text-sm text-gray-600">{summary}</span>
      </div>
      {dimensions && dimensions.length > 0 && (
        <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3">
          {dimensions.map((d) => (
            <div key={d.name} className="rounded border border-gray-100 bg-gray-50 p-2">
              <div className="mb-1 flex items-center justify-between">
                <span className="text-xs font-medium text-gray-700">{d.label}</span>
                <span className={`text-xs font-bold ${scoreColor(d.score).split(' ')[0]}`}>
                  {d.score.toFixed(1)}
                </span>
              </div>
              {/* 进度条 */}
              <div className="mb-1 h-1.5 w-full overflow-hidden rounded-full bg-gray-200">
                <div
                  className={`h-full ${scoreBarColor(d.score)}`}
                  style={{ width: `${(d.score / 10) * 100}%` }}
                />
              </div>
              {/* 扣分原因（可能为空） */}
              {d.note && (
                <p className="text-xs leading-relaxed text-gray-500">{d.note}</p>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
