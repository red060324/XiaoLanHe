import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import App from './App';

const api = vi.hoisted(() => ({
  getMe: vi.fn(),
  listGames: vi.fn(),
  getGame: vi.fn(),
  login: vi.fn(),
  logout: vi.fn(),
  register: vi.fn(),
  streamChatMessage: vi.fn()
}));

vi.mock('./lib/api', () => api);

beforeEach(() => {
  window.localStorage.clear();
  api.getMe.mockResolvedValue(null);
  api.listGames.mockResolvedValue([]);
  api.streamChatMessage.mockResolvedValue(undefined);
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('App', () => {
  it('shows the catalog empty state', async () => {
    render(<App />);
    fireEvent.click(screen.getByRole('button', { name: '游戏库' }));
    expect(await screen.findByText('没有找到游戏。')).toBeInTheDocument();
  });

  it('cancels the active stream before starting a new chat', async () => {
    let signal: AbortSignal | undefined;
    api.streamChatMessage.mockImplementation((_payload, _onChunk, requestSignal: AbortSignal) => {
      signal = requestSignal;
      return new Promise<void>((_resolve, reject) => requestSignal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError'))));
    });
    render(<App />);
    fireEvent.change(screen.getByPlaceholderText('问攻略、版本或社区内容'), { target: { value: '攻略' } });
    fireEvent.click(screen.getByRole('button', { name: '发送' }));
    await waitFor(() => expect(api.streamChatMessage).toHaveBeenCalledOnce());
    fireEvent.click(screen.getByRole('button', { name: '游戏助手' }));
    expect(signal?.aborted).toBe(true);
  });
});
