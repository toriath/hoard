import { Ellipsis, type LucideIcon } from 'lucide-react'
import { link, navigate } from '../lib/router'
import { Menu } from '../ui'

export interface NavItem {
  to: string
  label: string
  icon: LucideIcon
  match: (path: string) => boolean
}

/** How many destinations get their own button on a phone; the rest go behind "More". */
export const PRIMARY_NAV = 4

/**
 * Phone navigation: the most important destinations as buttons, everything
 * else in a menu. "More" shows as current when one of its pages is open.
 */
export function BottomNav({ items, path }: { items: NavItem[]; path: string }) {
  const primary = items.slice(0, PRIMARY_NAV)
  const more = items.slice(PRIMARY_NAV)
  const moreCurrent = more.some((n) => n.match(path))
  return (
    <nav className="bottomnav" aria-label="Main">
      {primary.map((n) => (
        <a key={n.to} aria-current={n.match(path) ? 'page' : undefined} {...link(n.to)}>
          <n.icon aria-hidden />
          <span>{n.label}</span>
        </a>
      ))}
      <Menu
        align="right"
        trigger={(p) => (
          <button type="button" {...p} aria-haspopup="menu" aria-current={moreCurrent ? 'page' : undefined}>
            <Ellipsis aria-hidden />
            <span>More</span>
          </button>
        )}
        items={more.map((n) => ({ label: n.label, icon: n.icon, onSelect: () => navigate(n.to) }))}
      />
    </nav>
  )
}
