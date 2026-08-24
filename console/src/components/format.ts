/** RFC 3339 时间转本地时区紧凑文本。 */
export function formatTime(rfc3339: string | undefined): string {
  if (rfc3339 === undefined || rfc3339 === '') return '—'
  const d = new Date(rfc3339)
  if (Number.isNaN(d.getTime())) return rfc3339
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

/** Duration 文本转紧凑中文时长。 */
export function formatDuration(duration: string | undefined): string {
  if (duration === undefined || duration === '') return '—'
  const seconds = Number(duration.replace(/s$/, ''))
  if (!Number.isFinite(seconds)) return duration
  if (seconds % 86400 === 0) return `${seconds / 86400} 天`
  if (seconds % 3600 === 0) return `${seconds / 3600} 小时`
  return `${seconds} 秒`
}
