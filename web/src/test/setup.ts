import { afterEach, vi } from 'vitest';
import { cleanup } from '@testing-library/react';

// jsdom has no EventSource or scrollTo; the console uses both.
class FakeEventSource {
  addEventListener() {}
  close() {}
}
vi.stubGlobal('EventSource', FakeEventSource);
window.scrollTo = () => {};

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  window.location.hash = '';
});
