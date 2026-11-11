import { Download, Pencil, Plus, Search, Sparkles, Trash2, Upload } from 'lucide-react'
import { useState } from 'react'
import { getMeta, toast } from '../lib/store'
import { SeverityIcon } from './panels/Fit'
import { Button, Check, Chip, CodeBlock, Dialog, Empty, Field, Menu, Tabs, TagInput, TypeBadge, typeColor, typeIcon } from '../ui'

const COLORS = ['bg', 'surface-1', 'surface-2', 'surface-3', 'border', 'border-strong', 'text', 'text-muted', 'text-faint', 'accent', 'accent-hover', 'accent-soft', 'danger', 'success', 'warning', 'info']
const SPACE = [1, 2, 3, 4, 5, 6, 8, 10, 12]
const TYPE_SCALE: [string, string][] = [['3xl', 'Display'], ['2xl', 'Page title'], ['xl', 'Section title'], ['lg', 'Emphasis'], ['md', 'Body / UI default'], ['sm', 'Secondary'], ['xs', 'Eyebrow / badge']]

/** Living reference of the design system: every token and primitive in one place. */
export function DesignSystem() {
  const meta = getMeta()
  const [tags, setTags] = useState(['pixel-art', 'sword'])
  const [on, setOn] = useState(true)
  const [tab, setTab] = useState('one')
  const [dialog, setDialog] = useState(false)
  return (
    <div className="page page-narrow ds">
      <div className="page-head">
        <div>
          <h1>Design system</h1>
          <p className="muted">
            Tokens live in <code>web/src/styles/tokens.css</code>, primitives in <code>components.css</code> and <code>src/ui</code>. Views compose these and never invent colours, sizes or controls.
          </p>
        </div>
      </div>

      <section className="stack">
        <h2>Colour</h2>
        <div className="ds-swatches">
          {COLORS.map((c) => (
            <div key={c}>
              <i style={{ background: `var(--${c})` }} />
              <span className="mono">--{c}</span>
            </div>
          ))}
        </div>
        <h3>Asset types</h3>
        <p className="muted">Each type owns one hue, used for its badge, icon and waveform. It is the fastest way to scan a mixed grid.</p>
        <div className="row-wrap">
          {meta.types.map((t) => {
            const Icon = typeIcon(t.id)
            return (
              <span key={t.id} className="ds-type" style={{ color: typeColor(t.id) }}>
                <Icon aria-hidden /> <TypeBadge type={t.id} />
              </span>
            )
          })}
        </div>
      </section>

      <section className="stack">
        <h2>Typography</h2>
        {TYPE_SCALE.map(([k, label]) => (
          <div key={k} className="row">
            <span className="mono faint" style={{ width: 90 }}>--fs-{k}</span>
            <span style={{ fontSize: `var(--fs-${k})`, fontWeight: k.includes('xl') ? 650 : 400 }}>{label}</span>
          </div>
        ))}
        <p className="eyebrow">Eyebrow label</p>
        <p className="mono">Monospace for ids, hashes and code: ast_7k2m9q4x1c8v</p>
      </section>

      <section className="stack">
        <h2>Spacing &amp; shape</h2>
        <div className="row-wrap" style={{ alignItems: 'flex-end' }}>
          {SPACE.map((s) => (
            <div key={s} className="ds-space">
              <i style={{ width: `var(--sp-${s})`, height: `var(--sp-${s})` }} />
              <span className="mono faint">{s}</span>
            </div>
          ))}
        </div>
        <div className="row-wrap">
          {['sm', 'md', 'lg', 'full'].map((r) => (
            <span key={r} className="ds-radius mono" style={{ borderRadius: `var(--r-${r})` }}>
              --r-{r}
            </span>
          ))}
        </div>
      </section>

      <section className="stack">
        <h2>Buttons</h2>
        <div className="row-wrap">
          <Button variant="primary" icon={Upload}>Primary</Button>
          <Button icon={Pencil}>Default</Button>
          <Button variant="ghost">Ghost</Button>
          <Button variant="danger" icon={Trash2}>Danger</Button>
          <Button disabled>Disabled</Button>
          <Button busy>Busy</Button>
          <Button iconOnly icon={Download} aria-label="Download" />
          <Button aria-pressed>Pressed</Button>
        </div>
        <div className="row-wrap">
          <Button size="sm" icon={Sparkles}>Small</Button>
          <Button>Medium</Button>
          <Button size="lg" variant="primary">Large</Button>
        </div>
        <p className="muted">One primary action per view. Destructive actions are outlined, never filled, and always confirm when they cannot be undone.</p>
      </section>

      <section className="stack">
        <h2>Form controls</h2>
        <div className="field-row">
          <Field label="Text input" hint="Hints sit below the control">
            <input className="input" placeholder="Placeholder" />
          </Field>
          <Field label="Select">
            <select className="select">
              <option>Option one</option>
              <option>Option two</option>
            </select>
          </Field>
        </div>
        <Field label="Textarea" error="Errors replace the hint">
          <textarea className="textarea" rows={2} defaultValue="Multi-line text" />
        </Field>
        <Field label="Tag input">
          <TagInput value={tags} onChange={setTags} />
        </Field>
        <div className="row-wrap">
          <Check checked={on} onChange={setOn}>Checkbox</Check>
          <Check switch checked={on} onChange={setOn}>Switch</Check>
        </div>
      </section>

      <section className="stack">
        <h2>Chips, badges, menus</h2>
        <div className="row-wrap">
          <Chip>tag</Chip>
          <Chip active>active filter</Chip>
          <Chip onRemove={() => {}}>removable</Chip>
          <Chip onClick={() => {}}>clickable <b>12</b></Chip>
          <span className="badge badge-plain">plain badge</span>
          <span className="kbd">/</span>
          <Menu
            align="left"
            trigger={(p) => <Button icon={Plus} {...p}>Menu</Button>}
            items={[{ label: 'First action', icon: Pencil }, { label: 'Second action', icon: Download }, { label: '', separator: true }, { label: 'Destructive', icon: Trash2, danger: true }]}
          />
        </div>
        <Tabs value={tab} onChange={setTab} tabs={[{ id: 'one', label: 'Tab one' }, { id: 'two', label: 'Tab two' }, { id: 'three', label: 'Tab three' }]} />
      </section>

      <section className="stack">
        <h2>Feedback</h2>
        <div className="note">Informational note</div>
        <div className="note note-ok">Success note</div>
        <div className="note note-warn">Warning note</div>
        <div className="note note-danger">Error note</div>
        <div className="row-wrap">
          <Button onClick={() => toast('This is a toast')}>Show toast</Button>
          <Button onClick={() => toast('Something failed', 'error')}>Show error toast</Button>
          <Button onClick={() => setDialog(true)}>Open dialog</Button>
          <span className="spinner" />
        </div>
        <div className="progress" style={{ maxWidth: 320 }}>
          <i style={{ width: '62%' }} />
        </div>
        <div className="card">
          <Empty icon={Search} title="Empty state" action={<Button variant="primary">Primary action</Button>}>
            Say what belongs here and offer the one action that fills it.
          </Empty>
        </div>
        <CodeBlock code={'claude mcp add --transport http hoard http://nas.local:8080/mcp'} />
      </section>

      <section className="stack">
        <h2>Brief and fit</h2>
        <div className="row-wrap">
          <span className="swatches">
            {['#1a1c2c', '#5d275d', '#b13e53', '#ef7d57', '#ffcd75'].map((c) => (
              <span key={c} className="swatch" style={{ background: c }} title={c} />
            ))}
          </span>
          <span className="muted">Palette swatches</span>
        </div>
        <details className="fit" open>
          <summary>
            <SeverityIcon severity="error" />
            <span className="grow">Fit finding group</span>
            <span className="muted">1 error, 1 warning</span>
          </summary>
          <ul className="fit-list">
            <li><SeverityIcon severity="error" /><span>44100 Hz, project wants 48000 Hz.</span></li>
            <li><SeverityIcon severity="warning" /><span>Image is 100x100 px; the project wants power-of-two dimensions.</span></li>
            <li><SeverityIcon severity="info" /><span>Total size 12.3 kB (0.0% of the 200 MB budget).</span></li>
          </ul>
        </details>
      </section>

      {dialog && (
        <Dialog title="Dialog" onClose={() => setDialog(false)} footer={<><Button onClick={() => setDialog(false)}>Cancel</Button><Button variant="primary" onClick={() => setDialog(false)}>Confirm</Button></>}>
          <p className="muted">Dialogs centre on desktop and become bottom sheets on phones. Escape or a click on the backdrop closes them.</p>
        </Dialog>
      )}
    </div>
  )
}
