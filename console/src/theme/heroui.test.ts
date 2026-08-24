import { describe, expect, it } from 'vitest'

import { buildHerouiThemes } from './heroui'

describe('buildHerouiThemes', () => {
  it('只生成正式圆融主题及其安全状态色', () => {
    const themes = buildHerouiThemes()

    expect(Object.keys(themes)).toEqual(['fusionr'])
    expect(themes.fusionr).toMatchObject({
      extend: 'dark',
      layout: {
        radius: { small: '8px', medium: '12px', large: '16px' },
      },
      colors: {
        background: '#0a0d10',
        foreground: '#e9edf0',
        focus: '#62e6a7',
        primary: { DEFAULT: '#62e6a7', foreground: '#0a1f14' },
        warning: { DEFAULT: '#f1be5b', foreground: '#0b0b10' },
        danger: { DEFAULT: '#ff746c', foreground: '#ffffff' },
      },
    })
    expect(JSON.stringify(themes)).not.toContain('gallery')
  })
})
