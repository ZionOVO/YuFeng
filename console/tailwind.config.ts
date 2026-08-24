import { existsSync } from 'node:fs'
import { heroui } from '@heroui/react'
import type { Config } from 'tailwindcss'
import { buildHerouiThemes } from './src/theme/heroui'

// @heroui/theme 可能被 npm 嵌套在 @heroui/react 之下而未提升到顶层，
// 主题类名扫描路径必须指到实际位置，否则组件样式整体缺失。
const themeDistCandidates = [
  './node_modules/@heroui/theme/dist/**/*.{js,ts,jsx,tsx,mjs}',
  './node_modules/@heroui/react/node_modules/@heroui/theme/dist/**/*.{js,ts,jsx,tsx,mjs}',
]
const themeDist =
  themeDistCandidates.find((p) => existsSync(p.replace(/\*\*.*$/, ''))) ?? themeDistCandidates[0]

export default {
  content: ['./index.html', './src/**/*.{ts,tsx}', themeDist],
  theme: { extend: {} },
  darkMode: 'class',
  plugins: [heroui({ themes: buildHerouiThemes() })],
} satisfies Config
