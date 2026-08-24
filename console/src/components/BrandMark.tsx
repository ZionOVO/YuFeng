// BrandMark 渲染仓库标准盾形标识（public/yufeng-logo.png），供登录、引导与控制台壳层共用。

export function BrandMark({ size = 38, decorative = true }: { size?: number; decorative?: boolean }) {
  const src = `${import.meta.env.BASE_URL}yufeng-logo.png`
  return (
    <img
      src={src}
      width={size}
      height={size}
      alt={decorative ? '' : '御锋'}
      aria-hidden={decorative}
      className="yf-brand-mark"
      style={{ width: size, height: size }}
    />
  )
}
