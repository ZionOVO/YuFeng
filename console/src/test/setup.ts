import '@testing-library/jest-dom/vitest'
import { configure } from '@testing-library/react'

// 低性能开发机上的异步页面切换可能超过库默认的一秒；单测试总时限仍识别真正挂起。
configure({ asyncUtilTimeout: 5_000 })

// jsdom 缺失的浏览器 API 兜底：HeroUI 组件（Table / Modal / Select）会触及这些接口。
if (!window.matchMedia) {
  window.matchMedia = (query: string): MediaQueryList => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  })
}

if (typeof window.ResizeObserver === 'undefined') {
  window.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {}
}

// jsdom 不实现 2d context；拓扑图画布在测试里只测 DOM，不测像素。
HTMLCanvasElement.prototype.getContext = () => null
