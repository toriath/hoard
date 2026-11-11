import { Component, StrictMode, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './App'
import { applyTheme, getTheme } from './lib/store'
import './styles/tokens.css'
import './styles/components.css'
import './styles/app.css'

applyTheme(getTheme())

/** Last line of defence: a rendering error shows a message instead of a blank page. */
class ErrorBoundary extends Component<{ children: ReactNode }, { error: Error | null }> {
  state = { error: null as Error | null }
  static getDerivedStateFromError(error: Error) {
    return { error }
  }
  render() {
    if (!this.state.error) return this.props.children
    return (
      <main className="auth">
        <div className="auth-card card" style={{ textAlign: 'center' }}>
          <h2>Something went wrong</h2>
          <p className="muted">The page hit an unexpected error. Your data is not affected.</p>
          <pre className="codeblock" style={{ textAlign: 'left' }}>{this.state.error.message}</pre>
          <button className="btn btn-primary" onClick={() => window.location.assign('/')}>
            Reload
          </button>
        </div>
      </main>
    )
  }
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ErrorBoundary>
      <App />
    </ErrorBoundary>
  </StrictMode>,
)
