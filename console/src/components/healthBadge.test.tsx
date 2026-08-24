import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { HealthBadge } from './ui'

describe('HealthBadge', () => {
  it('侧载静默与偏斜写成执行面可能看不见', () => {
    const { rerender } = render(<HealthBadge health="tap_silent" />)
    expect(screen.getByText('执行面可能看不见')).toBeInTheDocument()
    rerender(<HealthBadge health="UNIT_HEALTH_TAP_SKEW" />)
    expect(screen.getByText('执行面可能看不见')).toBeInTheDocument()
  })

  it('健康与未知分开', () => {
    const { rerender } = render(<HealthBadge health="healthy" />)
    expect(screen.getByText('健康')).toBeInTheDocument()
    rerender(<HealthBadge health="nope" />)
    expect(screen.getByText('未知')).toBeInTheDocument()
  })
})
