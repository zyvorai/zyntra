import { Component, type ReactNode } from 'react';

interface State {
  error: Error | null;
}

/**
 * Catches a render crash in a page and shows what happened instead of a blank
 * screen. It resets when the page (its key) changes, so navigating away
 * recovers without a reload.
 */
export default class ErrorBoundary extends Component<{ children: ReactNode; label: string }, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error) {
    // Keep it in the console too, for a bug report.
    console.error('page crashed:', error);
  }

  render() {
    if (!this.state.error) return this.props.children;
    return (
      <section className="card" role="alert">
        <h2>{this.props.label} could not be shown</h2>
        <p className="muted">
          This page hit an unexpected error and stopped drawing. The rest of the console still works; use the menu to go elsewhere, or reload to try again.
        </p>
        <p className="mono small">{this.state.error.message}</p>
        <button className="btn-secondary" onClick={() => window.location.reload()}>
          Reload
        </button>
      </section>
    );
  }
}
