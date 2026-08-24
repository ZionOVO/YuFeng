// 与 lib/kernel/limits.go 同名同值的前端常量（docs/api.md §19 / architecture §13）。
// 页面与测试只引用这些常量，禁止另写模型名或超时秒数。

/** 引导未指定模型名时的缺省聊天模型。 */
export const DefaultChatModel = 'grok-4-1-fast-non-reasoning'

/** 引导缺省模型端点（仅 brain 出网）。 */
export const DefaultModelBaseURL = 'https://api.x.ai/v1'

/** GetOnboarding.jarvis_online 判定窗口（秒）。 */
export const JarvisOnlineWindowSeconds = 60

/** PollMessages 省略 long_poll_seconds 时的等待（秒）。 */
export const SessionLongPollDefault = 30

/** PollMessages.long_poll_seconds 上限（秒）。 */
export const SessionLongPollMax = 60

/** 列表页大小上限，与 lib/kernel.PageSizeMax 同值（docs/api.md §0.6）。 */
export const PageSizeMax = 200
