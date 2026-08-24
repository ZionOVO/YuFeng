// 工具导图：§6.1 授予表按类展开，编排原语单独一枝且不可勾选。
// 左列是可访问的代码树，右列是同一份数据的思维导图。

import { useMemo, useState } from 'react'
import { TOOL_BRANCHES, findToolLeaf, type ToolLeaf } from './catalog'

export interface ToolTreeProps {
  mode?: 'browse' | 'select'
  selected?: string[]
  onToggle?: (name: string) => void
}

interface NodePos {
  x: number
  y: number
}

const ROOT: NodePos = { x: 56, y: 0 }
const BRANCH_X = 200
const LEAF_X = 430

export function ToolTree({ mode = 'browse', selected = [], onToggle }: ToolTreeProps) {
  const [picked, setPicked] = useState<string | null>(null)
  const leaf = picked !== null ? findToolLeaf(picked) : undefined

  const layout = useMemo(() => {
    const branchY: Record<string, number> = {}
    const leafPos: { leaf: ToolLeaf; branchId: string; y: number }[] = []
    let y = 28
    for (const b of TOOL_BRANCHES) {
      const start = y
      for (const t of b.tools) {
        leafPos.push({ leaf: t, branchId: b.id, y })
        y += 28
      }
      branchY[b.id] = (start + y - 28) / 2
      y += 18
    }
    const height = Math.max(y, 240)
    return { branchY, leafPos, height, rootY: height / 2 }
  }, [])

  const pick = (name: string, grantable: boolean) => {
    setPicked(name)
    if (mode === 'select' && grantable) onToggle?.(name)
  }

  return (
    <div className="yf-tooltree" aria-label="工具导图">
      <div className="yf-tooltree-rail" aria-label="工具目录">
        {TOOL_BRANCHES.map((b) => (
          <div key={b.id} className="yf-tooltree-dir">
            <p className="yf-tooltree-dir-name">
              {b.grantable ? '' : '// '}
              {b.id}
            </p>
            <ul>
              {b.tools.map((t) => {
                const on = selected.includes(t.name)
                return (
                  <li key={t.name}>
                    <button
                      type="button"
                      className={`yf-tooltree-file${on ? ' is-on' : ''}${t.grantable ? '' : ' is-locked'}`}
                      aria-label={t.grantable ? `工具 ${t.name}` : `编排原语 ${t.name}`}
                      aria-pressed={mode === 'select' && t.grantable ? on : undefined}
                      onClick={() => pick(t.name, t.grantable)}
                    >
                      {t.name}
                    </button>
                  </li>
                )
              })}
            </ul>
          </div>
        ))}
      </div>
      <div className="yf-tooltree-map">
        <svg viewBox={`0 0 560 ${layout.height}`} width="100%" height={layout.height} preserveAspectRatio="xMinYMin meet" role="img" aria-label="工具分类导图">
          {TOOL_BRANCHES.map((b) => {
            const by = layout.branchY[b.id] ?? 0
            return (
              <path
                key={`rb-${b.id}`}
                d={`M ${ROOT.x + 36} ${layout.rootY} C ${ROOT.x + 90} ${layout.rootY}, ${BRANCH_X - 70} ${by}, ${BRANCH_X} ${by}`}
                className="yf-tooltree-link"
              />
            )
          })}
          {layout.leafPos.map((n) => {
            const by = layout.branchY[n.branchId] ?? n.y
            return (
              <path
                key={`bl-${n.leaf.name}`}
                d={`M ${BRANCH_X + 44} ${by} C ${BRANCH_X + 90} ${by}, ${LEAF_X - 60} ${n.y}, ${LEAF_X} ${n.y}`}
                className={`yf-tooltree-link${selected.includes(n.leaf.name) ? ' is-on' : ''}`}
              />
            )
          })}
          <g className="yf-tooltree-root" transform={`translate(${ROOT.x}, ${layout.rootY})`}>
            <circle r="22" />
            <text y="4">工具</text>
          </g>
          {TOOL_BRANCHES.map((b) => (
            <g key={b.id} className={`yf-tooltree-hub${b.grantable ? '' : ' is-locked'}`} transform={`translate(${BRANCH_X}, ${layout.branchY[b.id] ?? 0})`}>
              <circle r="16" />
              <text y="4">{b.label}</text>
            </g>
          ))}
        </svg>
        {layout.leafPos.map((n) => (
          <button
            key={n.leaf.name}
            type="button"
            className={`yf-tooltree-leaf${selected.includes(n.leaf.name) ? ' is-on' : ''}${n.leaf.grantable ? '' : ' is-locked'}${picked === n.leaf.name ? ' is-pick' : ''}`}
            style={{ top: n.y, left: LEAF_X }}
            tabIndex={-1}
            aria-hidden
            onClick={() => pick(n.leaf.name, n.leaf.grantable)}
          >
            {n.leaf.name.split('.').pop()}
          </button>
        ))}
      </div>
      <aside className="yf-tooltree-ins">
        {leaf === undefined ? (
          <p className="yf-tooltree-empty">点一个名字。授予页只勾选实线那几枝；编排原语进不了授予表。</p>
        ) : (
          <>
            <p className="yf-tooltree-kicker">{leaf.grantable ? '授予表' : '编排原语'}</p>
            <h3>{leaf.name}</h3>
            <p>{leaf.blurb}</p>
            {mode === 'select' && leaf.grantable && (
              <p className="yf-tooltree-state">{selected.includes(leaf.name) ? '已勾选' : '未勾选 · 再点一次目录即可'}</p>
            )}
            {mode === 'select' && !leaf.grantable && <p className="yf-tooltree-state">不能授给人。</p>}
          </>
        )}
      </aside>
    </div>
  )
}
