import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HeroUIProvider } from '@heroui/react'
import type { ReactElement } from 'react'
import { ToolTree } from './ToolTree'

function mount(ui: ReactElement) {
  return render(
    <HeroUIProvider>
      <div className="fusionr">{ui}</div>
    </HeroUIProvider>,
  )
}

describe('工具导图', () => {
  it('授予表工具可勾选，编排原语不可授', async () => {
    const user = userEvent.setup()
    const onToggle = vi.fn()
    mount(<ToolTree mode="select" selected={[]} onToggle={onToggle} />)
    expect(screen.getByRole('img', { name: '工具分类导图' })).toBeInTheDocument()
    await user.click(screen.getByLabelText('工具 govern.promote_enforce'))
    expect(onToggle).toHaveBeenCalledWith('govern.promote_enforce')
    await user.click(screen.getByLabelText('编排原语 session.reply'))
    expect(onToggle).not.toHaveBeenCalledWith('session.reply')
    expect(screen.getByText('不能授给人。')).toBeInTheDocument()
  })
})
