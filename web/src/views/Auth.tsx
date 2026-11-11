import { useState } from 'react'
import { api, ApiError } from '../lib/api'
import { Button, Field } from '../ui'

export function Logo({ size = 28 }: { size?: number }) {
  return (
    <svg className="logo" width={size} height={size} viewBox="0 0 32 32" aria-hidden>
      <path d="M16 4.5 26 10.5v11L16 27.5 6 21.5v-11z" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinejoin="round" />
      <path d="M6.5 10.5 16 16l9.5-5.5M16 16v11" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinejoin="round" />
      <circle cx="16" cy="16" r="2.4" fill="currentColor" />
    </svg>
  )
}

/** Sign-in screen; doubles as first-run setup when no account exists yet. */
export function AuthScreen({ setup, onDone }: { setup: boolean; onDone: () => void }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (setup && password !== confirm) {
      setError('The passwords do not match')
      return
    }
    setBusy(true)
    setError('')
    try {
      await (setup ? api.setup(username, password) : api.login(username, password))
      onDone()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Something went wrong')
    } finally {
      setBusy(false)
    }
  }

  return (
    <main className="auth">
      <form className="auth-card card" onSubmit={submit}>
        <div className="auth-brand">
          <Logo size={44} />
          <h1>hoard</h1>
          <p className="muted">{setup ? 'Welcome. Create the first account to get started.' : 'Your game-asset vault'}</p>
        </div>
        <Field label="Username">
          <input className="input" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" autoCapitalize="none" autoFocus required />
        </Field>
        <Field label="Password" hint={setup ? 'At least 10 characters' : undefined}>
          <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete={setup ? 'new-password' : 'current-password'} minLength={setup ? 10 : undefined} required />
        </Field>
        {setup && (
          <Field label="Repeat password">
            <input className="input" type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="new-password" required />
          </Field>
        )}
        {error && (
          <div className="note note-danger" role="alert">
            {error}
          </div>
        )}
        <Button type="submit" variant="primary" size="lg" block busy={busy}>
          {setup ? 'Create account' : 'Sign in'}
        </Button>
      </form>
    </main>
  )
}
