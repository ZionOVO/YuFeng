// 404 页：渲染在主题壳之外（App.tsx 的 * 路由不经 ThemeScope），根元素自带 fusionr dark 主题类与整页背景。

import { Button } from '@heroui/react'
import { Link } from 'react-router-dom'

export function NotFoundPage() {
  return (
    <div className="fusionr dark flex min-h-screen items-center justify-center bg-[#0a0d10] text-[#e9edf0]">
      <div className="flex flex-col items-center gap-4 text-center">
        <p className="fs-mono text-6xl font-semibold">404</p>
        <p className="text-sm text-[#8b98a1]">页面不存在</p>
        <Button as={Link} to="/dashboard" size="sm" radius="md" color="primary" variant="bordered">
          返回仪表盘
        </Button>
      </div>
    </div>
  )
}
