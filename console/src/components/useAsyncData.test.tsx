import { act, renderHook, waitFor } from '@testing-library/react'
import { useState } from 'react'
import { useAsyncData } from './useAsyncData'

describe('useAsyncData 对象切换', () => {
  it('详情依赖变化时立即隐藏旧对象，同一对象 reload 时仍保留旧投影', async () => {
    const pending = new Map<string, (value: string) => void>()
    const load = (id: string) => new Promise<string>((resolve) => pending.set(id, resolve))
    const { result } = renderHook(() => {
      const [id, setId] = useState('asset-1')
      return { id, setId, query: useAsyncData(() => load(id), [id], false) }
    })

    await act(async () => pending.get('asset-1')?.('first'))
    expect(result.current.query.data).toBe('first')

    act(() => result.current.query.reload())
    expect(result.current.query.data).toBe('first')
    await act(async () => pending.get('asset-1')?.('first-refreshed'))
    await waitFor(() => expect(result.current.query.data).toBe('first-refreshed'))

    act(() => result.current.setId('asset-2'))
    expect(result.current.query.status).toBe('loading')
    expect(result.current.query.data).toBeNull()
    await act(async () => pending.get('asset-2')?.('second'))
    await waitFor(() => expect(result.current.query.data).toBe('second'))
  })
})
