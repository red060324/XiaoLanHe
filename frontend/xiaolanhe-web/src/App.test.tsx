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
  streamChatMessage: vi.fn(),
  claimCoupon: vi.fn(),
  createOrder: vi.fn(),
  listDeals: vi.fn(),
  listOrders: vi.fn(),
  payOrder: vi.fn(),
  createCommunityComment: vi.fn(),
  createCommunityPost: vi.fn(),
  deleteCommunityComment: vi.fn(),
  deleteCommunityPost: vi.fn(),
  getCommunityPost: vi.fn(),
  listCommunityComments: vi.fn(),
  listCommunityPosts: vi.fn(),
  setCommunityReaction: vi.fn(),
  updateCommunityComment: vi.fn(),
  updateCommunityPost: vi.fn()
}));

vi.mock('./lib/api', () => api);

beforeEach(() => {
  window.localStorage.clear();
  api.getMe.mockResolvedValue(null);
  api.listGames.mockResolvedValue([]);
  api.listCommunityPosts.mockResolvedValue({ items: [] });
  api.listDeals.mockResolvedValue({ items: [] });
  api.listOrders.mockResolvedValue({ items: [] });
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

  it('opens the community from the primary navigation', async () => {
    render(<App />);
    fireEvent.click(screen.getByRole('button', { name: /游戏社区/ }));

    expect(await screen.findByText('攻略、体验和版本讨论。')).toBeInTheDocument();
    expect(api.listCommunityPosts).toHaveBeenCalledWith('', '');
  });

  it('opens the public deals and purchase page', async () => {
    render(<App />);
    fireEvent.click(screen.getByRole('button', { name: /优惠商店/ }));

    expect(await screen.findByText('领取优惠券，按服务器价格创建订单，并使用沙箱支付解锁游戏。')).toBeInTheDocument();
    expect(api.listDeals).toHaveBeenCalledWith('');
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
