// 前后端共享的常量与类型。
//
// 研究进度的实时推送走 WebSocket（见 api/research.ts 的 WSMessage），
// 消息结构与后端 api.wsMessage 对齐。

// 研究状态的阶段标识（与后端 researcher.Stage 对齐）。
export const Stage = {
  Role: 'role',
  Planning: 'planning',
  Searching: 'searching',
  Fetching: 'fetching',
  Compressing: 'compressing',
  Writing: 'writing',
} as const
