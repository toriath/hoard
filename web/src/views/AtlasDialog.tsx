import { Download } from 'lucide-react'
import { useState } from 'react'
import { fetchAtlas } from '../lib/api-delivery'
import { plural } from '../lib/format'
import { toast, toastError } from '../lib/store'
import type { AtlasOptions } from '../lib/types-delivery'
import { Button, Check, Dialog, Field } from '../ui'

const MAX_ASSETS = 256
const clamp = (v: string, lo: number, hi: number) => Math.min(hi, Math.max(lo, Math.round(Number(v) || 0)))

/** Library selection action: pack the primary images of the selection into a texture atlas. */
export function AtlasDialog({ ids, onClose }: { ids: string[]; onClose: () => void }) {
  const [o, setO] = useState<AtlasOptions>({ padding: 1, max_size: 2048, power_of_two: false, extrude: 0, format: 'json', res_path: 'res://atlas.png' })
  // Number fields keep what is typed; they are clamped on blur and on submit.
  const [text, setText] = useState({ max_size: '2048', padding: '1', extrude: '0' })
  const [busy, setBusy] = useState(false)
  const set = <K extends keyof AtlasOptions>(k: K, v: AtlasOptions[K]) => setO((p) => ({ ...p, [k]: v }))
  const limits = { max_size: [16, 8192], padding: [0, 32], extrude: [0, 4] } as const
  type Num = keyof typeof limits
  const settle = (k: Num) => {
    const v = clamp(text[k], limits[k][0], limits[k][1])
    setText((t) => ({ ...t, [k]: String(v) }))
    set(k, v)
    return v
  }
  const num = (k: Num, label: string, hint?: string) => (
    <Field label={label} hint={hint}>
      <input
        className="input"
        inputMode="numeric"
        value={text[k]}
        onChange={(e) => setText((t) => ({ ...t, [k]: e.target.value }))}
        onBlur={() => settle(k)}
      />
    </Field>
  )

  const tooMany = ids.length > MAX_ASSETS
  const valid = /^res:\/\/[A-Za-z0-9_\-./ ]{1,200}\.png$/.test(o.res_path) && !o.res_path.includes('..')
  const ok = !tooMany && (o.format !== 'godot' || valid)

  async function download() {
    const final = { ...o, max_size: settle('max_size'), padding: settle('padding'), extrude: settle('extrude') }
    setBusy(true)
    try {
      const blob = await fetchAtlas(ids, final)
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = 'atlas.zip'
      document.body.append(a)
      a.click()
      a.remove()
      setTimeout(() => URL.revokeObjectURL(url), 10_000)
      toast('Atlas packed')
      onClose()
    } catch (e) {
      toastError(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog
      title="Pack atlas"
      onClose={onClose}
      width={460}
      footer={
        <Button variant="primary" icon={Download} busy={busy} disabled={!ok} onClick={download}>
          Download zip
        </Button>
      }
    >
      <p className="muted">
        Packs the main image of {plural(ids.length, 'asset')} into one PNG with frame data. Only PNG, JPEG, GIF, WebP, BMP and TIFF files can be packed; at most {MAX_ASSETS} assets.
      </p>
      {tooMany && <p role="alert">Too many assets selected: choose at most {MAX_ASSETS}.</p>}
      <Field label="Metadata format" hint="JSON: generic frames · Phaser: JSON hash · Godot: one AtlasTexture .tres per sprite">
        <select className="select" value={o.format} onChange={(e) => set('format', e.target.value as AtlasOptions['format'])}>
          <option value="json">Generic JSON</option>
          <option value="phaser">Phaser (JSON hash)</option>
          <option value="godot">Godot 4 (AtlasTexture)</option>
        </select>
      </Field>
      {o.format === 'godot' && (
        <Field label="Atlas path in the project" error={valid ? undefined : 'Use a path like res://art/atlas.png'}>
          <input className="input" value={o.res_path} onChange={(e) => set('res_path', e.target.value)} />
        </Field>
      )}
      <div className="atlas-grid">
        {num('max_size', 'Max size (px)')}
        {num('padding', 'Padding (px)')}
        {num('extrude', 'Extrude (px)', 'Repeats edge pixels')}
      </div>
      <Check checked={o.power_of_two} onChange={(v) => set('power_of_two', v)}>
        Round the size up to powers of two
      </Check>
    </Dialog>
  )
}
