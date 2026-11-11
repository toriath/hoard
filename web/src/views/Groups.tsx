import { Ellipsis, ExternalLink, FolderOpen, Gamepad2, Pencil, Plus, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { api } from '../lib/api'
import { plural } from '../lib/format'
import { link, navigate } from '../lib/router'
import { changed, getMeta, toast, toastError, useQuery, useVersion } from '../lib/store'
import type { Collection, Project } from '../lib/types'
import { Button, Dialog, Empty, Field, Menu, Spinner, useConfirm } from '../ui'
import { Library, ScopeHeader } from './Library'

type Kind = 'collection' | 'project'
type Group = Collection | Project

const isProject = (g: Group): g is Project => 'status' in g

function Covers({ covers, kind }: { covers: string[]; kind: Kind }) {
  const Icon = kind === 'collection' ? FolderOpen : Gamepad2
  if (covers.length === 0) {
    return (
      <div className="covers covers-empty">
        <Icon aria-hidden />
      </div>
    )
  }
  return (
    <div className="covers checker" data-n={covers.length}>
      {covers.map((c) => {
        const [id, v] = c.split(':')
        return <img key={id} src={`/api/v1/assets/${id}/thumbnail?v=${v}`} alt="" loading="lazy" />
      })}
    </div>
  )
}

export function GroupList({ kind }: { kind: Kind }) {
  const version = useVersion('groups')
  const assetsVersion = useVersion('assets')
  const q = useQuery<Group[]>(() => (kind === 'collection' ? api.collections() : api.projects()), [kind, version, assetsVersion])
  const [editing, setEditing] = useState<Group | 'new' | null>(null)
  const confirm = useConfirm()
  const base = kind === 'collection' ? '/collections' : '/projects'
  const Icon = kind === 'collection' ? FolderOpen : Gamepad2

  const remove = async (g: Group) => {
    if (!(await confirm({ title: `Delete ${kind}?`, message: `“${g.name}” will be deleted. Its ${plural(g.asset_count, 'asset')} stay in the library.`, confirmLabel: 'Delete', danger: true }))) return
    try {
      await (kind === 'collection' ? api.deleteCollection(g.id) : api.deleteProject(g.id))
      changed('groups')
      toast('Deleted')
    } catch (e) {
      toastError(e)
    }
  }

  return (
    <div className="page">
      <div className="page-head">
        <div className="grow">
          <h1>{kind === 'collection' ? 'Collections' : 'Projects'}</h1>
          <p className="muted">
            {kind === 'collection'
              ? 'Group assets by pack, visual style or whatever fits how you think.'
              : 'Track which assets ended up in which game.'}
          </p>
        </div>
        <Button variant="primary" icon={Plus} onClick={() => setEditing('new')}>
          New {kind}
        </Button>
      </div>

      {q.loading && !q.data ? (
        <Spinner />
      ) : (q.data ?? []).length === 0 ? (
        <Empty icon={Icon} title={`No ${kind}s yet`} action={<Button variant="primary" icon={Plus} onClick={() => setEditing('new')}>New {kind}</Button>}>
          {kind === 'collection'
            ? 'Create a collection, then add assets to it from the library with bulk edit or from an asset’s details.'
            : 'Create a project, then mark the assets you use in it. Claude can record usage for you over MCP.'}
        </Empty>
      ) : (
        <div className="group-grid">
          {(q.data ?? []).map((g) => (
            <article key={g.id} className="group-card card">
              <a {...link(`${base}/${g.id}`)} className="group-link" aria-label={g.name}>
                <Covers covers={g.covers} kind={kind} />
              </a>
              <div className="group-meta">
                <div className="grow">
                  <a {...link(`${base}/${g.id}`)} className="name truncate">
                    {g.name}
                  </a>
                  <div className="row-wrap muted">
                    <span className="badge badge-plain">{isProject(g) ? g.status : g.kind}</span>
                    <span>{plural(g.asset_count, 'asset')}</span>
                  </div>
                  {g.description && <p className="desc muted">{g.description}</p>}
                </div>
                <Menu
                  trigger={(p) => <Button variant="ghost" size="sm" iconOnly icon={Ellipsis} aria-label="Actions" {...p} />}
                  items={[
                    { label: 'Edit', icon: Pencil, onSelect: () => setEditing(g) },
                    ...(isProject(g) && g.url ? [{ label: 'Open project link', icon: ExternalLink, href: g.url }] : []),
                    { label: 'Delete', icon: Trash2, danger: true, onSelect: () => remove(g) },
                  ]}
                />
              </div>
            </article>
          ))}
        </div>
      )}
      {editing && <GroupDialog kind={kind} group={editing === 'new' ? null : editing} onClose={() => setEditing(null)} />}
    </div>
  )
}

export function GroupDialog({ kind, group, onClose }: { kind: Kind; group: Group | null; onClose: () => void }) {
  const meta = getMeta()
  const [name, setName] = useState(group?.name ?? '')
  const [description, setDescription] = useState(group?.description ?? '')
  const [cKind, setCKind] = useState(group && !isProject(group) ? group.kind : 'pack')
  const [url, setUrl] = useState(group && isProject(group) ? group.url : '')
  const [status, setStatus] = useState(group && isProject(group) ? group.status : 'active')
  const [saving, setSaving] = useState(false)

  const save = async () => {
    setSaving(true)
    try {
      await (kind === 'collection'
        ? api.saveCollection(group?.id ?? null, { name, description, kind: cKind })
        : api.saveProject(group?.id ?? null, { name, description, url, status }))
      changed('groups')
      toast(group ? 'Saved' : 'Created')
      onClose()
    } catch (e) {
      toastError(e)
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog
      title={group ? `Edit ${kind}` : `New ${kind}`}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" busy={saving} disabled={!name.trim()} onClick={save}>
            {group ? 'Save' : 'Create'}
          </Button>
        </>
      }
    >
      <div className="stack">
        <Field label="Name">
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} autoFocus placeholder={kind === 'collection' ? 'e.g. Dungeon Tiles Pack' : 'e.g. Starfall Tactics'} />
        </Field>
        <Field label="Description">
          <textarea className="textarea" rows={2} value={description} onChange={(e) => setDescription(e.target.value)} />
        </Field>
        {kind === 'collection' ? (
          <Field label="Kind" hint="Pack: assets that ship together · Style: a shared look · Project: a set planned for one game">
            <select className="select" value={cKind} onChange={(e) => setCKind(e.target.value)}>
              {meta.collection_kinds.map((k) => (
                <option key={k} value={k}>
                  {k[0].toUpperCase() + k.slice(1)}
                </option>
              ))}
            </select>
          </Field>
        ) : (
          <div className="field-row">
            <Field label="Status">
              <select className="select" value={status} onChange={(e) => setStatus(e.target.value)}>
                {meta.project_statuses.map((k) => (
                  <option key={k} value={k}>
                    {k[0].toUpperCase() + k.slice(1)}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="Link">
              <input className="input" type="url" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://github.com/…" />
            </Field>
          </div>
        )}
      </div>
    </Dialog>
  )
}

export function GroupDetail({ kind, id }: { kind: Kind; id: string }) {
  const version = useVersion('groups')
  const assetsVersion = useVersion('assets')
  const q = useQuery<Group[]>(() => (kind === 'collection' ? api.collections() : api.projects()), [kind, version, assetsVersion])
  const [editing, setEditing] = useState(false)
  const group = q.data?.find((g) => g.id === id)
  const base = kind === 'collection' ? '/collections' : '/projects'

  if (q.loading && !q.data) return <Spinner />
  if (!group) {
    return (
      <div className="page">
        <Empty icon={kind === 'collection' ? FolderOpen : Gamepad2} title={`This ${kind} does not exist`} action={<Button onClick={() => navigate(base)}>Back</Button>} />
      </div>
    )
  }
  return (
    <>
      <Library
        scope={kind === 'collection' ? { collection: id } : { project: id }}
        emptyHint={
          kind === 'collection'
            ? 'Upload here, or select assets in the library and use Edit → Add to collection.'
            : 'Upload here, or select assets in the library and use Edit → Used in project.'
        }
        header={
          <ScopeHeader
            kind={kind}
            name={group.name}
            description={group.description}
            badge={isProject(group) ? group.status : group.kind}
            count={group.asset_count}
            backTo={base}
            actions={
              <>
                {isProject(group) && group.url && (
                  <a className="btn" href={group.url} target="_blank" rel="noreferrer noopener" aria-label="Open project link">
                    <ExternalLink aria-hidden /> <span className="hide-sm">Open</span>
                  </a>
                )}
                <Button icon={Pencil} aria-label="Edit" onClick={() => setEditing(true)}>
                  <span className="hide-sm">Edit</span>
                </Button>
              </>
            }
          />
        }
      />
      {editing && <GroupDialog kind={kind} group={group} onClose={() => setEditing(false)} />}
    </>
  )
}
