import { fileURLToPath, URL } from 'node:url'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

// 交付由 brain 把静态产物托管在 /app（docs/api.md §17.1）。
// 开发与交付运行时都只连接真实 brain。
export default defineConfig({
  base: '/app/',
  plugins: [react()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  server: {
    // 开发期把 RPC 路径代理到本地 brain。
    proxy: {
      '/yufeng': {
        target: process.env.VITE_BRAIN_URL ?? 'https://127.0.0.1:9050',
        changeOrigin: true,
        // 只影响本机开发代理；交付控制台与 brain 同源，不经过这里。
        secure: false,
      },
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: './src/test/setup.ts',
    css: false,
    // 页面测试包含异步渲染；给低性能开发机保留调度余量，工作流级超时仍负责识别真正挂起。
    testTimeout: 15_000,
    hookTimeout: 15_000,
    coverage: {
      provider: 'v8',
      reporter: ['text', 'json-summary'],
      include: ['src/**/*.{ts,tsx}'],
      exclude: [
        'src/**/*.test.{ts,tsx}',
        'src/test/**',
        'src/main.tsx',
        'src/vite-env.d.ts',
        // 客户端契约只有 TypeScript 类型声明，编译后没有可执行代码。
        'src/api/client.ts',
      ],
      thresholds: {
        statements: 75,
        branches: 65,
        functions: 75,
        lines: 75,
      },
    },
  },
})
