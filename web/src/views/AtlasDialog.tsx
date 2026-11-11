import { Download } from 'lucide-react'
import { useState } from 'react'
import { deliveryApi } from '../lib/api-delivery'
import { plural } from '../lib/format'
import type { AtlasOptions } from '../lib/types-delivery'
import { Check, Dialog, Field } from '../ui'

const num = (v: string, lo: number, hi: number) => Math.min(hi, Math.max(lo, Math.round(Number(v) || 0)))

/** Library selection action: pack the primary images of the selection into a texture atlas. */
export function AtlasDialog({ ids, onClose }: { ids: string[]; onClose: () => void }) {
  const [o, setO] = useState<AtlasOptions>({ padding: 1, max_size: 2048, power_of_two: false, extrude: 0, format: 'json', res_path: 'res://atlas.png' })
  const set = <K extends keyof AtlasOptions>(k: K, v: AtlasOptions[K]) => setO((p) => ({ ...p, [k]: v }))
  const valid = /^res:\/\/[A-Za-z0-9_\-./ ]{1,200}\.png$/.test(o.res_path) && !o.res_path.includes('..')
  const ok = o.format !== 'godot' || valid

  return (
    <Dialog
      title="Pack atlas"
      onClose={onClose}
      width={460}
      footer={
        ok ? (
          <a className="btn btn-primary" href={deliveryApi.atlasUrl(ids, o)} download="atlas.zip" onClick={onClose}>
            <Download aria-hidden />
            Download zip
          </a>
        ) : (
          <button type="button" className="btn btn-primary" disabled>
            <Download aria-hidden />
            Download zip
          </button>
        )
      }
    >
      <p className="muted">
        Packs the main image of {plural(ids.length, 'asset')} into one PNG with frame data. Only PNG, JPEG, GIF, WebP, BMP and TIFF files can be packed; at most 256 assets.
      </p>
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
        <Field label="Max size (px)">
          <input className="input" type="number" min={16} max={8192} value={o.max_size} onChange={(e) => set('max_size', num(e.target.value, 16, 8192))} />
        </Field>
        <Field label="Padding (px)">
          <input className="input" type="number" min={0} max={32} value={o.padding} onChange={(e) => set('padding', num(e.target.value, 0, 32))} />
        </Field>
        <Field label="Extrude (px)" hint="Repeats edge pixels">
          <input className="input" type="number" min={0} max={4} value={o.extrude} onChange={(e) => set('extrude', num(e.target.value, 0, 4))} />
        </Field>
      </div>
      <Check checked={o.power_of_two} onChange={(v) => set('power_of_two', v)}>
        Round the size up to powers of two
      </Check>
    </Dialog>
  )
}
