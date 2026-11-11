import { Activity as ActivityIcon, ClipboardList, FolderOpen, Gamepad2, Library as LibraryIcon, Settings as SettingsIcon, Tags as TagsIcon, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { api, setUnauthorizedHandler } from './lib/api'
import { stopPreview } from './lib/audio'
import { link, useLocation } from './lib/router'
import { getMeta, refreshMeta, useQuery, useVersion } from './lib/store'
import { ConfirmProvider, Spinner, Toasts, cx, typeColor } from './ui'
import { ActivityPage } from './views/Activity'
import { AssetDrawer } from './views/AssetDetail'
import { AuthScreen, Logo } from './views/Auth'
import { DesignSystem } from './views/DesignSystem'
import { currentAssetId, openAsset } from './views/filters'
import { GroupDetail, GroupList } from './views/Groups'
import { Library } from './views/Library'
import { RequestsPage } from './views/Requests'
import { Settings } from './views/Settings'
import { TagsPage } from './views/Tags'
import { UploadProvider } from './views/Upload'

type Session = 'loading' | 'setup' | 'signed-out' | 'ready' | 'offline'

export function App() {
  const [session, setSession] = useState<Session>('loading')

  const boot = useCallback(async () => {
    try {
      const s = await api.status()
      if (s.setup_required) return setSession('setup')
      if (!s.user) return setSession('signed-out')
      await refreshMeta()
      setSession('ready')
    } catch {
      setSession('offline')
    }
  }, [])

  useEffect(() => {
    setUnauthorizedHandler(() => setSession((cur) => (cur === 'ready' ? 'signed-out' : cur)))
    boot()
  }, [boot])

  return (
    <>
      {session === 'loading' && (
        <main className="auth">
          <Spinner />
        </main>
      )}
      {session === 'offline' && (
        <main className="auth">
          <div className="auth-card card" style={{ textAlign: 'center' }}>
            <h2>Cannot reach hoard</h2>
            <p className="muted">The server did not answer. Check that the container is running.</p>
            <button className="btn btn-primary" onClick={() => { setSession('loading'); boot() }}>
              Try again
            </button>
          </div>
        </main>
      )}
      {(session === 'setup' || session === 'signed-out') && <AuthScreen setup={session === 'setup'} onDone={boot} />}
      {session === 'ready' && (
        <ConfirmProvider>
          <UploadProvider>
            <Shell onSignedOut={() => setSession('signed-out')} />
          </UploadProvider>
        </ConfirmProvider>
      )}
      <Toasts />
    </>
  )
}

const NAV = [
  { to: '/', label: 'Library', icon: LibraryIcon, match: (p: string) => p === '/' || p.startsWith('/assets/') },
  { to: '/collections', label: 'Collections', icon: FolderOpen, match: (p: string) => p.startsWith('/collections') },
  { to: '/projects', label: 'Projects', icon: Gamepad2, match: (p: string) => p.startsWith('/projects') },
  { to: '/requests', label: 'Requests', icon: ClipboardList, match: (p: string) => p.startsWith('/requests') },
  { to: '/tags', label: 'Tags', icon: TagsIcon, match: (p: string) => p === '/tags' },
  { to: '/activity', label: 'Activity', icon: ActivityIcon, match: (p: string) => p === '/activity' },
  { to: '/trash', label: 'Trash', icon: Trash2, match: (p: string) => p === '/trash' },
  { to: '/settings', label: 'Settings', icon: SettingsIcon, match: (p: string) => p.startsWith('/settings') || p === '/design' },
]

function Shell({ onSignedOut }: { onSignedOut: () => void }) {
  const loc = useLocation()
  const assetId = currentAssetId(loc)
  const meta = getMeta()
  const stats = useQuery(() => api.stats(), [useVersion('assets')])

  // Stop audio previews when moving between pages.
  useEffect(() => stopPreview, [loc.path])

  const seg = loc.path.split('/').filter(Boolean)
  let page
  if (seg[0] === 'collections') page = seg[1] ? <GroupDetail key={seg[1]} kind="collection" id={seg[1]} /> : <GroupList kind="collection" />
  else if (seg[0] === 'projects') page = seg[1] ? <GroupDetail key={seg[1]} kind="project" id={seg[1]} /> : <GroupList kind="project" />
  else if (seg[0] === 'requests') page = <RequestsPage />
  else if (seg[0] === 'tags') page = <TagsPage />
  else if (seg[0] === 'activity') page = <ActivityPage />
  else if (seg[0] === 'trash') page = <Library key="trash" scope={{ trashed: true }} header={<div className="page-head"><div><h1>Trash</h1><p className="muted">{meta.trash_days > 0 ? `Trashed assets are deleted for good after ${meta.trash_days} days.` : 'Trashed assets stay here until you delete them for good.'}</p></div></div>} />
  else if (seg[0] === 'settings') page = <Settings tab={seg[1] ?? 'connect'} onSignedOut={onSignedOut} />
  else if (seg[0] === 'design') page = <DesignSystem />
  else page = <Library key="library" />

  const activeTypes = loc.path === '/' ? (loc.query.get('type') ?? '').split(',').filter(Boolean) : []

  return (
    <div className={cx('app', assetId && 'with-drawer')}>
      <nav className="sidebar" aria-label="Main">
        <a className="brand" {...link('/')}>
          <Logo />
          <span>hoard</span>
        </a>
        <div className="nav">
          {NAV.map((n) => (
            <a key={n.to} className="nav-item" aria-current={n.match(loc.path) ? 'page' : undefined} {...link(n.to)}>
              <n.icon aria-hidden />
              <span>{n.label}</span>
              {n.to === '/' && stats.data ? <span className="count">{stats.data.assets.toLocaleString()}</span> : null}
              {n.to === '/trash' && stats.data?.trashed ? <span className="count">{stats.data.trashed}</span> : null}
            </a>
          ))}
        </div>
        <div className="nav nav-types">
          <p className="eyebrow">Types</p>
          {meta.types
            .filter((t) => stats.data?.by_type[t.id])
            .map((t) => (
              <a key={t.id} className="nav-item nav-type" aria-current={activeTypes.length === 1 && activeTypes[0] === t.id ? 'page' : undefined} {...link(`/?type=${t.id}`)}>
                <span className="dot" style={{ background: typeColor(t.id) }} />
                <span>{t.label}</span>
                <span className="count">{stats.data?.by_type[t.id]}</span>
              </a>
            ))}
          {stats.data && stats.data.assets === 0 && <p className="faint nav-hint">Types appear here once you add assets.</p>}
        </div>
        <div className="sidebar-foot faint">
          <span className="truncate">{meta.user.username}</span>
          <span className="mono">{meta.version}</span>
        </div>
      </nav>

      <main className="main" id="main">
        {page}
      </main>

      {assetId && (
        <>
          <div className="drawer-scrim" onClick={() => openAsset(loc, null)} />
          <AssetDrawer key={assetId} id={assetId} />
        </>
      )}

      <nav className="bottomnav" aria-label="Main">
        {NAV.map((n) => (
          <a key={n.to} aria-current={n.match(loc.path) ? 'page' : undefined} {...link(n.to)}>
            <n.icon aria-hidden />
            <span>{n.label}</span>
          </a>
        ))}
      </nav>

      <datalist id="license-list">
        {meta.licenses.map((l) => (
          <option key={l} value={l} />
        ))}
      </datalist>
    </div>
  )
}
