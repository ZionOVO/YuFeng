const darkText = '#0b0b10'

// buildHerouiThemes 只生成正式控制台使用的圆融主题。
// 交付构建不得携带设计回廊或未启用主题的展示元数据。
export function buildHerouiThemes(): Record<string, object> {
  return {
    fusionr: {
      extend: 'dark',
      layout: {
        radius: { small: '8px', medium: '12px', large: '16px' },
      },
      colors: {
        background: '#0a0d10',
        foreground: '#e9edf0',
        content1: '#11161a',
        content2: '#161c21',
        content3: '#161c21',
        content4: '#161c21',
        divider: '#161c21',
        focus: '#62e6a7',
        primary: { DEFAULT: '#62e6a7', foreground: '#0a1f14' },
        secondary: { DEFAULT: '#8b98a1', foreground: darkText },
        success: { DEFAULT: '#62e6a7', foreground: darkText },
        warning: { DEFAULT: '#f1be5b', foreground: darkText },
        danger: { DEFAULT: '#ff746c', foreground: '#ffffff' },
      },
    },
  }
}
