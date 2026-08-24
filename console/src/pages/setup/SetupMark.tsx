// 引导状态标：转圈收到完整圆环，再画出对勾。

export function SetupMark({ ok, label }: { ok: boolean; label: string }) {
  return (
    <div className={`yf-setup-mark${ok ? ' yf-setup-mark--ok' : ''}`} role="status" aria-label={label}>
      <span className="yf-setup-spin" aria-hidden />
      <svg className="yf-setup-done" viewBox="0 0 48 48" aria-hidden>
        <circle className="yf-setup-done-ring" cx="24" cy="24" r="18" />
        <path className="yf-setup-done-check" d="M16.5 24.5 L21.5 29.5 L32 18.5" />
      </svg>
    </div>
  )
}
