import { useEffect, type RefObject } from 'react'

const FOCUSABLE = 'a[href],button:not([disabled]),textarea:not([disabled]),input:not([disabled]),select:not([disabled]),[tabindex]:not([tabindex="-1"])'

// useDialogFocusTrap 把键盘焦点限定在当前移动对话层，关闭后还原触发点。
export function useDialogFocusTrap(open: boolean, container: RefObject<HTMLElement | null>) {
  useEffect(() => {
    if (!open || container.current === null) return
    const root = container.current
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null
    const focusables = () => [...root.querySelectorAll<HTMLElement>(FOCUSABLE)].filter((item) => !item.hidden && item.getAttribute('aria-hidden') !== 'true')
    const frame = window.requestAnimationFrame(() => {
      // 子组件的 autoFocus 先于焦点陷阱生效时保留其选择，避免把输入焦点抢回关闭按钮。
      if (document.activeElement instanceof HTMLElement && root.contains(document.activeElement)) return
      const preferred = root.querySelector<HTMLElement>('[autofocus],textarea:not([disabled]),input:not([disabled])')
      ;(preferred ?? focusables()[0])?.focus()
    })
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== 'Tab') return
      const items = focusables()
      if (items.length === 0) {
        event.preventDefault()
        root.focus()
        return
      }
      const first = items[0]
      const last = items[items.length - 1]
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault()
        first.focus()
      }
    }
    root.addEventListener('keydown', onKey)
    return () => {
      window.cancelAnimationFrame(frame)
      root.removeEventListener('keydown', onKey)
      previous?.focus()
    }
  }, [container, open])
}
