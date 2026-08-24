import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'

export default tseslint.config(
  { ignores: ['dist', 'node_modules'] },
  {
    files: ['**/*.{ts,tsx}'],
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    languageOptions: {
      ecmaVersion: 2023,
      globals: globals.browser,
    },
    plugins: {
      'react-hooks': reactHooks,
      'react-refresh': reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      'react-refresh/only-export-components': ['warn', { allowConstantExport: true }],
      // 任务红线：禁止 any 绕过类型检查
      '@typescript-eslint/no-explicit-any': 'error',
    },
  },
  // 配置文件（vite/tailwind/postcss/eslint 自身）运行在 Node 环境
  {
    files: ['*.config.ts', '*.config.js'],
    languageOptions: { globals: { ...globals.node } },
  },
)
