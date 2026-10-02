import { fireEvent, render, screen } from '@testing-library/react';
import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import ErrorBoundary from '../components/ErrorBoundary';

function Boom({ explode }: { explode: boolean }) {
  if (explode) throw new Error("Cannot read properties of null (reading 'length')");
  return <p>all fine</p>;
}

describe('ErrorBoundary', () => {
  it('shows what failed instead of a blank page, and keeps the rest of the screen', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {});
    render(
      <div>
        <nav>menu still here</nav>
        <ErrorBoundary label="Decision">
          <Boom explode />
        </ErrorBoundary>
      </div>,
    );
    expect(screen.getByRole('alert').textContent).toMatch(/Decision could not be shown/);
    expect(screen.getByText(/reading 'length'/)).toBeTruthy();
    expect(screen.getByText('menu still here')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Reload' })).toBeTruthy();
  });

  it('renders normally when nothing throws', () => {
    render(
      <ErrorBoundary label="Objects">
        <Boom explode={false} />
      </ErrorBoundary>,
    );
    expect(screen.getByText('all fine')).toBeTruthy();
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('recovers when the page (its key) changes, with no reload', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {});
    function Shell() {
      const [page, setPage] = useState('decision');
      return (
        <div>
          <button onClick={() => setPage('objects')}>go</button>
          <ErrorBoundary key={page} label={page}>
            <Boom explode={page === 'decision'} />
          </ErrorBoundary>
        </div>
      );
    }
    render(<Shell />);
    expect(screen.getByRole('alert')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'go' }));
    expect(screen.getByText('all fine')).toBeTruthy();
    expect(screen.queryByRole('alert')).toBeNull();
  });
});
