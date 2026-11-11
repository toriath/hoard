// hoard design system — React components.
// Thin wrappers over the classes in styles/components.css. Views compose
// these; they never restyle primitives ad hoc.

import {
  AudioLines, Blocks, Box, Check as CheckIcon, Clapperboard, CodeXml, Copy, File, Grid3x3, Image, Layers, Mic, MousePointerClick, Music,
  PersonStanding, Sparkles, Type, X, type LucideIcon,
} from 'lucide-react'
import {
  cloneElement, createContext, isValidElement, useCallback, useContext, useEffect, useId, useLayoutEffect, useRef, useState,
  type ButtonHTMLAttributes, type CSSProperties, type ReactElement, type ReactNode,
} from 'react'
import { createPortal } from 'react-dom'
import { api } from '../lib/api'
import { copyText } from '../lib/format'
import { toast, typeLabel, useToasts } from '../lib/store'

const cx = (...parts: (string | false | null | undefined)[]) => parts.filter(Boolean).join(' ')

// ---------- Button ----------

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: 'default' | 'primary' | 'ghost' | 'danger'
  size?: 'sm' | 'md' | 'lg'
  icon?: LucideIcon
  iconOnly?: boolean
  block?: boolean
  busy?: boolean
}

export function Button({ variant = 'default', size = 'md', icon: Icon, iconOnly, block, busy, className, children, ...rest }: ButtonProps) {
  return (
    <button
      type="button"
      {...rest}
      disabled={rest.disabled || busy}
      className={cx(
        'btn',
        variant !== 'default' && `btn-${variant}`,
        size !== 'md' && `btn-${size}`,
        iconOnly && 'btn-icon',
        block && 'btn-block',
        className,
      )}
    >
      {busy ? <span className="spinner" style={{ width: 14, height: 14 }} /> : Icon && <Icon aria-hidden />}
      {!iconOnly && children}
      {iconOnly && <span className="sr-only">{children ?? rest['aria-label'] ?? rest.title}</span>}
    </button>
  )
}

// ---------- Field ----------

export function Field({ label, hint, error, children }: { label: string; hint?: ReactNode; error?: string; children: ReactNode }) {
  const id = useId()
  // A <label> wrapped around several controls forwards clicks on its text to
  // the first of them, which for a tag input is the first tag's remove
  // button. So only a lone native control is tied to the label; anything
  // else becomes a labelled group.
  const single =
    isValidElement(children) && typeof children.type === 'string' && ['input', 'select', 'textarea'].includes(children.type)
  return (
    <div className="field">
      {single ? (
        <label className="label" htmlFor={id}>
          {label}
        </label>
      ) : (
        <span className="label" id={id}>
          {label}
        </span>
      )}
      {single ? (
        cloneElement(children as ReactElement<{ id?: string }>, { id })
      ) : (
        <div role="group" aria-labelledby={id} className="field-group">
          {children}
        </div>
      )}
      {error ? <span className="error">{error}</span> : hint && <span className="hint">{hint}</span>}
    </div>
  )
}

export function Check({ checked, onChange, children, switch: sw }: { checked: boolean; onChange: (v: boolean) => void; children?: ReactNode; switch?: boolean }) {
  return (
    <label className={cx('check', sw && 'switch')}>
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      {children && <span>{children}</span>}
    </label>
  )
}

// ---------- Dialog ----------

const FOCUSABLE = 'a[href], button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])'

export function Dialog({
  title, onClose, children, footer, width,
}: {
  title: ReactNode
  onClose: () => void
  children: ReactNode
  footer?: ReactNode
  width?: number
}) {
  const ref = useRef<HTMLDivElement>(null)
  const titleId = useId()
  // Callers usually pass a new function every render. Keeping it in a ref
  // lets the effect below run exactly once, so focus is placed once and not
  // yanked away on every re-render of the parent.
  const close = useRef(onClose)
  close.current = onClose
  // What had focus before the dialog rendered (read before autoFocus runs).
  const [opener] = useState(() => document.activeElement as HTMLElement | null)

  useEffect(() => {
    const el = ref.current
    const onKey = (e: KeyboardEvent) => {
      // With a dialog on top of another (a confirmation over the upload
      // dialog), only the top one reacts.
      const overlays = document.querySelectorAll('.overlay')
      if (el?.parentElement !== overlays[overlays.length - 1]) return
      if (e.key === 'Escape') {
        // An open menu or tag suggestion list closes first.
        if (document.querySelector('.menu') || (e.target instanceof Element && e.target.closest('[data-suggesting="true"]'))) return
        e.stopPropagation()
        close.current()
      } else if (e.key === 'Tab' && el) {
        // Keep keyboard focus inside the dialog.
        const items = [...el.querySelectorAll<HTMLElement>(FOCUSABLE)]
        if (items.length === 0) return
        const first = items[0]
        const last = items[items.length - 1]
        const active = document.activeElement
        if (!el.contains(active) || (e.shiftKey && active === first)) {
          e.preventDefault()
          ;(e.shiftKey ? last : first).focus()
        } else if (!e.shiftKey && active === last) {
          e.preventDefault()
          first.focus()
        }
      }
    }
    document.addEventListener('keydown', onKey, true)
    // Move focus in, unless a control inside asked for it with autoFocus.
    if (el && !el.contains(document.activeElement)) {
      // The first visible control of the content, not the header's close button
      // (and not a hidden file input, which cannot take focus).
      const target = [...el.querySelectorAll<HTMLElement>('.dialog-body :is(input, textarea, select, button), .dialog-foot button')].find(
        (c) => c.offsetParent !== null && !c.hasAttribute('disabled'),
      )
      ;(target ?? el).focus()
    }
    return () => {
      document.removeEventListener('keydown', onKey, true)
      opener?.focus?.()
    }
  }, [opener])

  return createPortal(
    <div className="overlay" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div
        className="dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        ref={ref}
        style={width ? ({ '--dialog-w': `${width}px` } as CSSProperties) : undefined}
      >
        <div className="dialog-head">
          <h3 className="grow truncate" id={titleId}>
            {title}
          </h3>
          <Button variant="ghost" size="sm" iconOnly icon={X} onClick={onClose} aria-label="Close" />
        </div>
        <div className="dialog-body">{children}</div>
        {footer && <div className="dialog-foot">{footer}</div>}
      </div>
    </div>,
    document.body,
  )
}

// ---------- Confirm (imperative) ----------

interface ConfirmOptions {
  title: string
  message: ReactNode
  confirmLabel?: string
  danger?: boolean
}
const ConfirmContext = createContext<(o: ConfirmOptions) => Promise<boolean>>(async () => false)
export const useConfirm = () => useContext(ConfirmContext)

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<(ConfirmOptions & { resolve: (v: boolean) => void }) | null>(null)
  const confirm = useCallback((o: ConfirmOptions) => new Promise<boolean>((resolve) => setState({ ...o, resolve })), [])
  const close = (v: boolean) => {
    state?.resolve(v)
    setState(null)
  }
  return (
    <ConfirmContext.Provider value={confirm}>
      {children}
      {state && (
        <Dialog
          title={state.title}
          onClose={() => close(false)}
          width={420}
          footer={
            <>
              <Button onClick={() => close(false)}>Cancel</Button>
              <Button variant={state.danger ? 'danger' : 'primary'} onClick={() => close(true)} autoFocus>
                {state.confirmLabel ?? 'Confirm'}
              </Button>
            </>
          }
        >
          <div className="muted">{state.message}</div>
        </Dialog>
      )}
    </ConfirmContext.Provider>
  )
}

// ---------- Menu (dropdown) ----------

export interface MenuItem {
  label: string
  icon?: LucideIcon
  onSelect?: () => void
  href?: string
  danger?: boolean
  separator?: boolean
  disabled?: boolean
}

export function Menu({ trigger, items, align = 'right' }: { trigger: (props: { onClick: () => void; 'aria-expanded': boolean }) => ReactNode; items: MenuItem[]; align?: 'left' | 'right' }) {
  const [open, setOpen] = useState(false)
  const wrap = useRef<HTMLSpanElement>(null)
  const menu = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState<CSSProperties>({})

  useLayoutEffect(() => {
    if (!open || !wrap.current) return
    const r = wrap.current.getBoundingClientRect()
    const mh = menu.current?.offsetHeight ?? 0
    const below = r.bottom + 6 + mh < window.innerHeight
    setPos({
      position: 'fixed',
      top: below ? r.bottom + 6 : undefined,
      bottom: below ? undefined : window.innerHeight - r.top + 6,
      ...(align === 'right' ? { right: Math.max(8, window.innerWidth - r.right) } : { left: Math.max(8, r.left) }),
    })
  }, [open, align])

  useEffect(() => {
    if (!open) return
    const items = () => [...(menu.current?.querySelectorAll<HTMLElement>('.menu-item:not(:disabled)') ?? [])]
    const trigger = wrap.current?.querySelector<HTMLElement>('button, a')
    items()[0]?.focus({ preventScroll: true })
    const onKey = (e: KeyboardEvent) => {
      const list = items()
      const i = list.indexOf(document.activeElement as HTMLElement)
      if (e.key === 'Escape' || e.key === 'Tab') {
        if (e.key === 'Escape') e.preventDefault()
        setOpen(false)
        trigger?.focus()
      } else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault()
        if (list.length) list[(i + (e.key === 'ArrowDown' ? 1 : -1) + list.length) % list.length].focus()
      } else if (e.key === 'Home' || e.key === 'End') {
        e.preventDefault()
        list[e.key === 'Home' ? 0 : list.length - 1]?.focus()
      }
    }
    const dismiss = (e: Event) => {
      const target = e.target as Node
      // Clicks on the trigger toggle; scrolling a long menu is not a dismissal.
      if (wrap.current?.contains(target) || menu.current?.contains(target)) return
      setOpen(false)
    }
    document.addEventListener('mousedown', dismiss)
    document.addEventListener('keydown', onKey)
    window.addEventListener('scroll', dismiss, true)
    window.addEventListener('resize', dismiss)
    return () => {
      document.removeEventListener('mousedown', dismiss)
      document.removeEventListener('keydown', onKey)
      window.removeEventListener('scroll', dismiss, true)
      window.removeEventListener('resize', dismiss)
    }
  }, [open])

  return (
    <span ref={wrap} style={{ display: 'inline-flex' }}>
      {trigger({ onClick: () => setOpen((o) => !o), 'aria-expanded': open })}
      {open &&
        createPortal(
          <div className="menu" role="menu" ref={menu} style={pos}>
            {items.map((it, i) =>
              it.separator ? (
                <div className="menu-sep" key={i} />
              ) : it.href ? (
                <a className="menu-item" role="menuitem" key={i} href={it.href} target="_blank" rel="noreferrer noopener" onClick={() => setOpen(false)}>
                  {it.icon && <it.icon aria-hidden />}
                  {it.label}
                </a>
              ) : (
                <button
                  className={cx('menu-item', it.danger && 'danger')}
                  role="menuitem"
                  key={i}
                  disabled={it.disabled}
                  onClick={() => {
                    setOpen(false)
                    it.onSelect?.()
                  }}
                >
                  {it.icon && <it.icon aria-hidden />}
                  {it.label}
                </button>
              ),
            )}
          </div>,
          document.body,
        )}
    </span>
  )
}

// ---------- Chips ----------

export function Chip({ children, onRemove, active, onClick, title }: { children: ReactNode; onRemove?: () => void; active?: boolean; onClick?: () => void; title?: string }) {
  const cls = cx('chip', active && 'chip-active')
  const remove = onRemove && (
    <button
      type="button"
      className="x"
      aria-label="Remove"
      onClick={(e) => {
        e.stopPropagation()
        onRemove()
      }}
    >
      <X />
    </button>
  )
  // Clickable and removable: two sibling buttons, never one inside the other.
  if (onClick && onRemove) {
    return (
      <span className={cls}>
        <button type="button" className="chip-main truncate" onClick={onClick} title={title}>
          {children}
        </button>
        {remove}
      </span>
    )
  }
  return onClick ? (
    <button type="button" className={cls} onClick={onClick} title={title}>
      <span className="truncate">{children}</span>
    </button>
  ) : (
    <span className={cls} title={title}>
      <span className="truncate">{children}</span>
      {remove}
    </span>
  )
}

// ---------- Asset types ----------

const typeIcons: Record<string, LucideIcon> = {
  sprite: Image, tileset: Grid3x3, ui: MousePointerClick, texture: Layers, model: Box, animation: PersonStanding,
  vfx: Sparkles, shader: CodeXml, sfx: AudioLines, music: Music, voice: Mic, video: Clapperboard, font: Type, other: File,
}
export const typeIcon = (type: string): LucideIcon => typeIcons[type] ?? Blocks
export const typeColor = (type: string) => `var(--type-${typeIcons[type] ? type : 'other'})`

export function TypeBadge({ type }: { type: string }) {
  return (
    <span className="badge" style={{ '--badge': typeColor(type) } as CSSProperties}>
      {typeLabel(type)}
    </span>
  )
}

// ---------- TagInput ----------

export function TagInput({ value, onChange, placeholder = 'Add tag…', autoFocus }: { value: string[]; onChange: (tags: string[]) => void; placeholder?: string; autoFocus?: boolean }) {
  const [text, setText] = useState('')
  const [suggestions, setSuggestions] = useState<string[]>([])
  const [active, setActive] = useState(0)
  const [focused, setFocused] = useState(false)
  const input = useRef<HTMLInputElement>(null)
  const listId = useId()

  const normalize = (t: string) =>
    t.trim().toLowerCase().replace(/[^\p{L}\p{N}\-_.:+#]+/gu, '-').replace(/-{2,}/g, '-').replace(/^-|-$/g, '')

  useEffect(() => {
    const q = normalize(text)
    if (!focused) return
    let live = true
    const timer = setTimeout(() => {
      api.tags(q, 12).then(
        (tags) => live && setSuggestions(tags.map((t) => t.tag).filter((t) => !value.includes(t))),
        () => {},
      )
    }, 120)
    setActive(0)
    return () => {
      live = false
      clearTimeout(timer)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [text, focused, value.join(',')])

  const add = (raw: string) => {
    const tags = raw.split(',').map(normalize).filter((t) => t && !value.includes(t))
    if (tags.length) onChange([...value, ...tags])
    setText('')
  }

  return (
    <div className="taginput" data-suggesting={focused && suggestions.length > 0} onClick={() => input.current?.focus()}>
      {value.map((t) => (
        <Chip key={t} onRemove={() => onChange(value.filter((x) => x !== t))}>
          {t}
        </Chip>
      ))}
      <input
        ref={input}
        value={text}
        autoFocus={autoFocus}
        placeholder={value.length ? '' : placeholder}
        aria-controls={listId}
        onChange={(e) => {
          // A comma commits everything before it (typing or pasting "a, b, c").
          const v = e.target.value
          const cut = v.lastIndexOf(',')
          if (cut < 0) return setText(v)
          add(v.slice(0, cut))
          setText(v.slice(cut + 1).trimStart())
        }}
        onFocus={() => setFocused(true)}
        onBlur={() => {
          setFocused(false)
          if (text.trim()) add(text)
        }}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault()
            if (text.trim() === '' && suggestions.length === 0) return
            add(text.trim() && active === 0 ? text : (suggestions[active - 1] ?? text))
          } else if (e.key === 'Backspace' && text === '' && value.length) {
            onChange(value.slice(0, -1))
          } else if (e.key === 'ArrowDown') {
            e.preventDefault()
            setActive((a) => Math.min(suggestions.length, a + 1))
          } else if (e.key === 'ArrowUp') {
            e.preventDefault()
            setActive((a) => Math.max(0, a - 1))
          } else if (e.key === 'Escape' && focused && suggestions.length) {
            e.stopPropagation()
            setSuggestions([])
          }
        }}
      />
      {focused && suggestions.length > 0 && (
        <div className="suggest" id={listId} role="listbox">
          {suggestions.map((s, i) => (
            <button
              type="button"
              className="menu-item"
              role="option"
              aria-selected={active === i + 1}
              data-active={active === i + 1}
              key={s}
              onMouseDown={(e) => {
                e.preventDefault()
                add(s)
              }}
            >
              {s}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

// ---------- misc ----------

export function Spinner({ label }: { label?: string }) {
  return (
    <div className="row muted" role="status" style={{ justifyContent: 'center', padding: 'var(--sp-8)' }}>
      <span className="spinner" />
      {label}
    </div>
  )
}

export function Empty({ icon: Icon, title, children, action }: { icon: LucideIcon; title: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className="empty">
      <Icon aria-hidden />
      <h3>{title}</h3>
      {children && <p style={{ maxWidth: 420 }}>{children}</p>}
      {action}
    </div>
  )
}

export function CopyButton({ text, label = 'Copy', size = 'sm' }: { text: string; label?: string; size?: 'sm' | 'md' }) {
  const [done, setDone] = useState(false)
  return (
    <Button
      size={size}
      icon={done ? CheckIcon : Copy}
      onClick={async () => {
        if (await copyText(text)) {
          setDone(true)
          setTimeout(() => setDone(false), 1500)
        } else toast('Copy failed: select the text manually', 'error')
      }}
    >
      {done ? 'Copied' : label}
    </Button>
  )
}

export function CodeBlock({ code, copy = true }: { code: string; copy?: boolean }) {
  return (
    <div style={{ position: 'relative' }}>
      <pre className="codeblock" style={copy ? { paddingRight: 92 } : undefined}>
        {code}
      </pre>
      {copy && (
        <div style={{ position: 'absolute', top: 8, right: 8 }}>
          <CopyButton text={code} />
        </div>
      )}
    </div>
  )
}

export function Tabs<T extends string>({ tabs, value, onChange }: { tabs: { id: T; label: string; icon?: LucideIcon }[]; value: T; onChange: (id: T) => void }) {
  return (
    <div className="tabs" role="tablist">
      {tabs.map((t) => (
        <button key={t.id} role="tab" className="tab" aria-selected={value === t.id} onClick={() => onChange(t.id)}>
          {t.icon && <t.icon aria-hidden />}
          {t.label}
        </button>
      ))}
    </div>
  )
}

export function Toasts() {
  const toasts = useToasts()
  return createPortal(
    <div className="toasts" aria-live="polite">
      {toasts.map((t) => (
        <div key={t.id} className={cx('toast', t.kind === 'error' && 'toast-error')}>
          {t.message}
        </div>
      ))}
    </div>,
    document.body,
  )
}

/** Debounces a fast-changing value (search boxes). */
export function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value)
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms)
    return () => clearTimeout(t)
  }, [value, ms])
  return v
}

export { cx }
