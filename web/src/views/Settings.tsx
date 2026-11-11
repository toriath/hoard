import { Bot, Info, KeyRound, LogOut, Palette, Plug, Plus, Trash2, User as UserIcon } from 'lucide-react'
import { useEffect, useState } from 'react'
import { api } from '../lib/api'
import { formatBytes, formatDate, plural, timeAgo } from '../lib/format'
import { link, navigate } from '../lib/router'
import { applyTheme, changed, getMeta, getTheme, refreshMeta, toast, toastError, typeLabel, useQuery, useVersion, type Theme } from '../lib/store'
import type { AISettings } from '../lib/types'
import { Button, Check, CodeBlock, Field, Spinner, Tabs, useConfirm } from '../ui'

type Tab = 'connect' | 'ai' | 'account' | 'about'

export function Settings({ tab, onSignedOut }: { tab: string; onSignedOut: () => void }) {
  const current: Tab = (['connect', 'ai', 'account', 'about'] as const).find((t) => t === tab) ?? 'connect'
  return (
    <div className="page page-narrow">
      <div className="page-head">
        <h1>Settings</h1>
      </div>
      <Tabs<Tab>
        value={current}
        onChange={(t) => navigate(`/settings/${t}`)}
        tabs={[
          { id: 'connect', label: 'Connect Claude', icon: Plug },
          { id: 'ai', label: 'AI agent', icon: Bot },
          { id: 'account', label: 'Account', icon: UserIcon },
          { id: 'about', label: 'About', icon: Info },
        ]}
      />
      <div className="settings-body stack">
        {current === 'connect' && <ConnectTab />}
        {current === 'ai' && <AITab />}
        {current === 'account' && <AccountTab onSignedOut={onSignedOut} />}
        {current === 'about' && <AboutTab />}
      </div>
    </div>
  )
}

// ---------- Connect (API tokens + MCP) ----------

function ConnectTab() {
  const version = useVersion('tokens')
  const tokens = useQuery(() => api.tokens(), [version])
  const confirm = useConfirm()
  const [name, setName] = useState('')
  const [fresh, setFresh] = useState<{ name: string; secret: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const [client, setClient] = useState<'code' | 'project' | 'desktop' | 'toolhive' | 'rest'>('code')

  const origin = window.location.origin
  const token = fresh?.secret ?? '<YOUR_TOKEN>'
  const insecure = origin.startsWith('http://')

  const create = async () => {
    setBusy(true)
    try {
      const r = await api.createToken(name.trim() || 'Claude Code')
      setFresh({ name: r.token.name, secret: r.secret })
      setName('')
      changed('tokens')
    } catch (e) {
      toastError(e)
    } finally {
      setBusy(false)
    }
  }

  const snippets = {
    code: `claude mcp add --transport http --scope user hoard ${origin}/mcp \\\n  --header "Authorization: Bearer ${token}"`,
    project: JSON.stringify(
      { mcpServers: { hoard: { type: 'http', url: `${origin}/mcp`, headers: { Authorization: 'Bearer ${HOARD_TOKEN}' } } } },
      null,
      2,
    ),
    desktop: JSON.stringify(
      {
        mcpServers: {
          hoard: {
            command: 'npx',
            args: ['-y', 'mcp-remote', `${origin}/mcp`, '--transport', 'http-only', ...(insecure ? ['--allow-http'] : []), '--header', 'Authorization:${HOARD_AUTH}'],
            env: { HOARD_AUTH: `Bearer ${token}` },
          },
        },
      },
      null,
      2,
    ),
    toolhive: `thv run ${origin}/mcp --name hoard --transport streamable-http \\\n  --remote-auth-bearer-token ${token}`,
    rest: `curl -H "Authorization: Bearer ${token}" "${origin}/api/v1/assets?q=sword&limit=5"`,
  }

  return (
    <>
      <section className="card card-pad stack">
        <div>
          <h2>1 · Create an API token</h2>
          <p className="muted">Tokens let Claude (or any script) work with the library over MCP and the REST API. They cannot manage accounts, tokens or the AI provider. Each client should get its own, so you can revoke them one by one.</p>
        </div>
        <div className="row">
          <input className="input grow" aria-label="Token name" placeholder="Token name, e.g. Claude Code on MacBook" value={name} onChange={(e) => setName(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && !busy && create()} maxLength={80} />
          <Button variant="primary" icon={Plus} busy={busy} onClick={create}>
            Create token
          </Button>
        </div>
        {fresh && (
          <div className="note note-ok" style={{ flexDirection: 'column' }}>
            <span>
              <b>{fresh.name}</b> created. Copy it now — it is shown only this once.
            </span>
            <CodeBlock code={fresh.secret} />
          </div>
        )}
        {tokens.loading && !tokens.data ? (
          <Spinner />
        ) : (
          (tokens.data ?? []).length > 0 && (
            <ul className="plain-list">
              {(tokens.data ?? []).map((t) => (
                <li key={t.id} className="row">
                  <KeyRound aria-hidden className="faint" />
                  <span className="grow">
                    <b>{t.name}</b> <span className="mono faint">{t.prefix}…</span>
                    <br />
                    <span className="faint">
                      created {timeAgo(t.created_at)} · {t.last_used_at ? `last used ${timeAgo(t.last_used_at)}` : 'never used'}
                    </span>
                  </span>
                  <Button
                    size="sm"
                    variant="danger"
                    icon={Trash2}
                    onClick={async () => {
                      if (!(await confirm({ title: 'Revoke token?', message: `Clients using “${t.name}” will lose access immediately.`, confirmLabel: 'Revoke', danger: true }))) return
                      api.deleteToken(t.id).then(() => changed('tokens'), toastError)
                    }}
                  >
                    Revoke
                  </Button>
                </li>
              ))}
            </ul>
          )
        )}
      </section>

      <section className="card card-pad stack">
        <div>
          <h2>2 · Add hoard to Claude</h2>
          <p className="muted">
            MCP endpoint: <code>{origin}/mcp</code> (Streamable HTTP). The snippets use the address you are browsing from, so they work the same on your LAN and over your tailnet.
            {!fresh && ' Create a token above to have it filled in.'}
          </p>
        </div>
        <Tabs
          value={client}
          onChange={setClient}
          tabs={[
            { id: 'code', label: 'Claude Code' },
            { id: 'project', label: '.mcp.json' },
            { id: 'desktop', label: 'Claude Desktop' },
            { id: 'toolhive', label: 'ToolHive' },
            { id: 'rest', label: 'REST API' },
          ]}
        />
        {client === 'code' && (
          <>
            <p className="muted">Run once in a terminal. <code>--scope user</code> makes hoard available in every project.</p>
            <CodeBlock code={snippets.code} />
            <p className="muted">Then ask Claude things like “find a pixel-art sword in my hoard and add it to this game” or “store these generated sprites in hoard with the prompts I used”.</p>
          </>
        )}
        {client === 'project' && (
          <>
            <p className="muted">
              Commit this as <code>.mcp.json</code> in a game repository to share the setup, and export <code>HOARD_TOKEN</code> in your shell so the secret stays out of git.
            </p>
            <CodeBlock code={snippets.project} />
          </>
        )}
        {client === 'desktop' && (
          <>
            <p className="muted">
              Claude Desktop → Settings → Developer → Edit Config, merge this into <code>claude_desktop_config.json</code> and restart. It bridges to hoard with <code>mcp-remote</code> (needs Node.js).
              {insecure && ' --allow-http is included because this address is plain HTTP on your private network.'}
            </p>
            <CodeBlock code={snippets.desktop} />
          </>
        )}
        {client === 'toolhive' && (
          <>
            <p className="muted">
              Run once. ToolHive proxies hoard as a remote workload, adds the token to every request and registers it with the AI clients you have enabled in ToolHive. Manage it afterwards with <code>thv list</code>, <code>thv stop hoard</code> and <code>thv rm hoard</code>.
            </p>
            <CodeBlock code={snippets.toolhive} />
            <p className="muted">
              To keep the token out of your shell history, save it to a file and pass <code>--remote-auth-bearer-token-file &lt;path&gt;</code> instead.
            </p>
          </>
        )}
        {client === 'rest' && (
          <>
            <p className="muted">The whole library is available under <code>/api/v1</code> with the same token. Managing accounts, tokens and the AI provider needs a browser session.</p>
            <CodeBlock code={snippets.rest} />
          </>
        )}
      </section>
    </>
  )
}

// ---------- AI agent ----------

const CLAUDE_MODELS = ['claude-opus-5-5', 'claude-sonnet-5-5', 'claude-haiku-5-5', 'claude-fable-5-2']

function AITab() {
  const q = useQuery(() => api.aiSettings(), [])
  const [s, setS] = useState<AISettings | null>(null)
  const [key, setKey] = useState('')
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState(false)
  const [result, setResult] = useState<{ ok: boolean; message: string } | null>(null)
  const [models, setModels] = useState<string[]>([])

  useEffect(() => {
    if (q.data) setS(q.data)
  }, [q.data])
  if (q.error && !s) return <div className="note note-danger">Could not load the AI settings: {q.error.message}</div>
  if (!s) return <Spinner />
  // A stored key belongs to the provider and address it was saved with. If
  // either is changed, saving discards it and it has to be entered again.
  const saved = q.data
  const keyKept = !!saved && s.has_api_key && saved.provider === s.provider && saved.base_url.replace(/\/+$/, '') === s.base_url.replace(/\/+$/, '')
  const keyHint = (fallback: string) =>
    keyKept
      ? 'A key is stored. Enter a new one to replace it.'
      : s.has_api_key
        ? 'The stored key is discarded when you save, because provider or address changed. Enter the key for the new one.'
        : fallback
  const removeKey = async () => {
    try {
      setS(await api.saveAI({ ...s, api_key: undefined, clear_api_key: true }))
      q.reload()
      toast('Stored key removed')
    } catch (e) {
      toastError(e)
    }
  }
  const set = <K extends keyof AISettings>(k: K, v: AISettings[K]) => {
    setS({ ...s, [k]: v })
    setResult(null)
  }

  const save = async (): Promise<boolean> => {
    setSaving(true)
    try {
      setS(await api.saveAI({ ...s, api_key: key || undefined }))
      setKey('')
      q.reload()
      await refreshMeta()
      return true
    } catch (e) {
      toastError(e)
      return false
    } finally {
      setSaving(false)
    }
  }
  const test = async () => {
    if (!(await save())) return
    setTesting(true)
    setResult(null)
    try {
      setResult(await api.testAI())
    } catch (e) {
      setResult({ ok: false, message: e instanceof Error ? e.message : 'failed' })
    } finally {
      setTesting(false)
    }
  }
  const loadModels = async () => {
    const r = await api.aiModels(s.base_url || 'http://localhost:1234/v1', key).catch(() => ({ items: [] as string[], error: 'request failed' }))
    setModels(r.items)
    if (r.error) toast(r.error, 'error')
    else if (r.items.length === 0) toast('The server reports no loaded models', 'error')
    else if (!s.model) set('model', r.items[0])
  }

  return (
    <section className="card card-pad stack">
      <div>
        <h2>Built-in AI agent</h2>
        <p className="muted">
          Looks at an asset’s preview, files and existing metadata and writes a name, description, tags and type. It only fills gaps unless told to overwrite. Run it from an asset (“AI fill”), on a selection (“AI tag”), or automatically on upload.
        </p>
      </div>
      <Field label="Provider">
        <select className="select" value={s.provider} onChange={(e) => { set('provider', e.target.value as AISettings['provider']); setModels([]) }}>
          <option value="">Off</option>
          <option value="anthropic">Claude API (Anthropic)</option>
          <option value="openai">Local model — LM Studio or any OpenAI-compatible server</option>
        </select>
      </Field>

      {s.provider === 'anthropic' && (
        <>
          <Field label="Claude API key" hint={keyHint('Create one at platform.claude.com. Stored on this server only and never sent back to the browser.')}>
            <div className="row">
              <input className="input mono grow" type="password" autoComplete="off" value={key} onChange={(e) => setKey(e.target.value)} placeholder={keyKept ? '•••••••••••• (stored)' : 'sk-ant-…'} aria-label="Claude API key" />
              {s.has_api_key && <Button onClick={removeKey}>Remove key</Button>}
            </div>
          </Field>
          <Field label="Model" hint="Opus 5.5 is the default. Haiku 5.5 is much cheaper and usually good enough for tagging.">
            <input className="input mono" list="claude-models" value={s.model} onChange={(e) => set('model', e.target.value)} placeholder="claude-opus-5-5" />
            <datalist id="claude-models">
              {CLAUDE_MODELS.map((m) => (
                <option key={m} value={m} />
              ))}
            </datalist>
          </Field>
        </>
      )}

      {s.provider === 'openai' && (
        <>
          <div className="note">
            <span>
              In LM Studio open <b>Developer → Local Server</b>, start it and enable <b>Serve on Local Network</b>. Because hoard runs in a container, <code>localhost</code> means the container itself: use the LAN or tailnet address of the machine running LM Studio. Pick a vision-capable model (for example Qwen-VL or Gemma 3) so previews can be analysed.
            </span>
          </div>
          <Field label="Server URL">
            <input className="input mono" type="url" value={s.base_url} onChange={(e) => set('base_url', e.target.value)} placeholder="http://192.168.1.50:1234/v1" />
          </Field>
          <Field label="Model" hint="Leave empty to use the first model the server has loaded.">
            <div className="row">
              <input className="input mono grow" list="local-models" value={s.model} onChange={(e) => set('model', e.target.value)} placeholder="auto" />
              <Button onClick={loadModels}>Load list</Button>
            </div>
            <datalist id="local-models">
              {models.map((m) => (
                <option key={m} value={m} />
              ))}
            </datalist>
          </Field>
          <Field label="API key" hint={keyHint('Usually not needed for LM Studio.')}>
            <div className="row">
              <input className="input mono grow" type="password" autoComplete="off" value={key} onChange={(e) => setKey(e.target.value)} placeholder={keyKept ? '•••••••••••• (stored)' : 'optional'} aria-label="API key" />
              {s.has_api_key && <Button onClick={removeKey}>Remove key</Button>}
            </div>
          </Field>
        </>
      )}

      {s.provider !== '' && (
        <>
          <Check switch checked={s.auto_analyze} onChange={(v) => set('auto_analyze', v)}>
            Analyse new uploads automatically
          </Check>
          <Check switch checked={s.vision} onChange={(v) => set('vision', v)}>
            Send preview images to the model (needs a vision-capable model)
          </Check>
          <Field label="House rules" hint="Optional guidance added to every request, e.g. your tagging conventions or preferred language.">
            <textarea className="textarea" rows={3} value={s.instructions} onChange={(e) => set('instructions', e.target.value)} placeholder="e.g. Always tag the art style. Use British spelling. Tag anything for the roguelike with 'emberfall'." />
          </Field>
        </>
      )}

      {result && <div className={`note ${result.ok ? 'note-ok' : 'note-danger'}`}>{result.message}</div>}
      <div className="row" style={{ justifyContent: 'flex-end' }}>
        {s.provider !== '' && (
          <Button busy={testing} onClick={test}>
            Save and test
          </Button>
        )}
        <Button variant="primary" busy={saving && !testing} onClick={async () => (await save()) && toast('AI settings saved')}>
          Save
        </Button>
      </div>
    </section>
  )
}

// ---------- Account ----------

function AccountTab({ onSignedOut }: { onSignedOut: () => void }) {
  const me = getMeta().user
  const [users, setUsers] = useState<Awaited<ReturnType<typeof api.users>>>([])
  const [theme, setTheme] = useState<Theme>(getTheme())
  const [pw, setPw] = useState({ current: '', next: '' })
  const [nu, setNu] = useState({ username: '', password: '' })
  const confirm = useConfirm()
  const reload = () => api.users().then(setUsers, toastError)
  useEffect(() => {
    reload()
  }, [])

  return (
    <>
      <section className="card card-pad stack">
        <div className="row">
          <div className="grow">
            <h2>{me.username}</h2>
            <p className="muted">Signed in</p>
          </div>
          <Button icon={LogOut} onClick={() => api.logout().then(onSignedOut, toastError)}>
            Sign out
          </Button>
        </div>
        <Field label="Appearance">
          <div className="seg" role="group">
            {(['auto', 'dark', 'light'] as const).map((t) => (
              <Button key={t} aria-pressed={theme === t} icon={t === 'auto' ? Palette : undefined} onClick={() => { applyTheme(t); setTheme(t) }}>
                {t === 'auto' ? 'System' : t[0].toUpperCase() + t.slice(1)}
              </Button>
            ))}
          </div>
        </Field>
      </section>

      <form
        className="card card-pad stack"
        onSubmit={(e) => {
          e.preventDefault()
          api.changePassword(pw.current, pw.next).then(() => {
            setPw({ current: '', next: '' })
            toast('Password changed. Other sessions were signed out.')
          }, toastError)
        }}
      >
        <h2>Change password</h2>
        <div className="field-row">
          <Field label="Current password">
            <input className="input" type="password" autoComplete="current-password" value={pw.current} onChange={(e) => setPw({ ...pw, current: e.target.value })} required />
          </Field>
          <Field label="New password" hint="At least 10 characters">
            <input className="input" type="password" autoComplete="new-password" minLength={10} value={pw.next} onChange={(e) => setPw({ ...pw, next: e.target.value })} required />
          </Field>
        </div>
        <div className="row" style={{ justifyContent: 'flex-end' }}>
          <Button type="submit" variant="primary">
            Change password
          </Button>
        </div>
      </form>

      <section className="card card-pad stack">
        <div>
          <h2>Users</h2>
          <p className="muted">Everyone who can sign in has full access to the library.</p>
        </div>
        <ul className="plain-list">
          {users.map((u) => (
            <li key={u.id} className="row">
              <UserIcon aria-hidden className="faint" />
              <span className="grow">
                <b>{u.username}</b> {u.id === me.id && <span className="faint">(you)</span>}
                <br />
                <span className="faint">since {formatDate(u.created_at)}</span>
              </span>
              {u.id !== me.id && (
                <Button
                  size="sm"
                  variant="danger"
                  icon={Trash2}
                  onClick={async () => {
                    if (await confirm({ title: 'Delete user?', message: `${u.username} and their API tokens will be removed.`, confirmLabel: 'Delete', danger: true }))
                      api.deleteUser(u.id).then(reload, toastError)
                  }}
                >
                  Delete
                </Button>
              )}
            </li>
          ))}
        </ul>
        <form
          className="row-wrap"
          onSubmit={(e) => {
            e.preventDefault()
            api.createUser(nu.username, nu.password).then(() => {
              setNu({ username: '', password: '' })
              toast('User added')
              reload()
            }, toastError)
          }}
        >
          <input className="input grow" style={{ flexBasis: 160 }} aria-label="New username" placeholder="New username" autoComplete="off" value={nu.username} onChange={(e) => setNu({ ...nu, username: e.target.value })} required />
          <input className="input grow" style={{ flexBasis: 160 }} type="password" aria-label="Password for the new user" placeholder="Password (10+ characters)" autoComplete="new-password" minLength={10} value={nu.password} onChange={(e) => setNu({ ...nu, password: e.target.value })} required />
          <Button type="submit" icon={Plus}>
            Add user
          </Button>
        </form>
      </section>
    </>
  )
}

// ---------- About ----------

function AboutTab() {
  const meta = getMeta()
  const stats = useQuery(() => api.stats(), [])
  const st = stats.data
  return (
    <>
      <section className="card card-pad stack">
        <h2>Library</h2>
        {!st ? (
          <Spinner />
        ) : (
          <>
            <div className="stat-grid">
              <div><b>{st.assets.toLocaleString()}</b><span>assets</span></div>
              <div><b>{st.files.toLocaleString()}</b><span>files</span></div>
              <div><b>{formatBytes(st.total_size)}</b><span>on disk</span></div>
              <div><b>{st.tags.toLocaleString()}</b><span>tags</span></div>
              <div><b>{st.collections}</b><span>collections</span></div>
              <div><b>{st.projects}</b><span>projects</span></div>
            </div>
            <div className="row-wrap">
              {Object.entries(st.by_type).sort((a, b) => b[1] - a[1]).map(([t, n]) => (
                <a key={t} className="chip" {...link(`/?type=${t}`)}>
                  {typeLabel(t)} <b>{n}</b>
                </a>
              ))}
            </div>
            <p className="muted">
              {plural(st.untagged, 'asset')} without tags · <a {...link('/?untagged=1')}>show</a> &nbsp;·&nbsp; {plural(st.trashed, 'asset')} in the <a {...link('/trash')}>trash</a>
            </p>
          </>
        )}
      </section>
      <section className="card card-pad stack">
        <h2>Where your data lives</h2>
        <p className="muted">Everything is stored below the container’s <code>/data</code> volume, in a layout you can browse, back up and restore with ordinary tools:</p>
        <CodeBlock copy={false} code={`/data
├── hoard.db                 SQLite index (metadata, tags, search, users)
├── assets/<asset-id>/
│   ├── asset.json           all metadata of the asset, kept in sync
│   ├── thumb.png|jpg        preview thumbnail
│   └── files/<filename>     your original files, untouched
└── backups/hoard-<date>.db  daily snapshots of the index`} />
        <p className="muted">Snapshot the dataset in TrueNAS and you have a complete, consistent backup.</p>
      </section>
      <section className="card card-pad stack">
        <h2>About</h2>
        <p className="muted">
          hoard <span className="mono">{meta.version}</span> · upload limit {formatBytes(meta.max_upload_bytes)} per file · <a {...link('/design')}>design system</a>
        </p>
      </section>
    </>
  )
}
