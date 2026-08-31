import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
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

  it('shows a failure message when the stream fails before replying', async () => {
    api.streamChatMessage.mockRejectedValue(new Error('assistant unavailable'));
    render(<App />);

    fireEvent.change(screen.getByPlaceholderText('问攻略、版本或社区内容'), { target: { value: '攻略' } });
    fireEvent.click(screen.getByRole('button', { name: '发送' }));

    expect(await screen.findByText('生成失败，请重试。')).toBeInTheDocument();
    expect(screen.getByText('请求失败：assistant unavailable')).toBeInTheDocument();
  });

  it('isolates local conversations when another account signs in', async () => {
    api.getMe.mockResolvedValue({ id: '1', username: 'alice', displayName: 'Alice', role: 'user' });
    api.login.mockResolvedValue({ id: '2', username: 'bob', displayName: 'Bob', role: 'user' });
    api.logout.mockResolvedValue(undefined);
    render(<App />);

    expect(await screen.findByRole('button', { name: 'Alice' })).toBeInTheDocument();
    fireEvent.change(screen.getByPlaceholderText('问攻略、版本或社区内容'), { target: { value: 'Alice 的私人攻略' } });
    fireEvent.click(screen.getByRole('button', { name: '发送' }));
    await waitFor(() => expect(api.streamChatMessage).toHaveBeenCalledOnce());
    const aliceSessionId = api.streamChatMessage.mock.calls[0][0].sessionId;

    fireEvent.click(screen.getByRole('button', { name: '退出登录' }));
    await waitFor(() => expect(api.logout).toHaveBeenCalledOnce());
    fireEvent.click(screen.getByRole('button', { name: '登录 / 注册' }));
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'bob' } });
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'password123' } });
    fireEvent.submit(screen.getByLabelText('用户名').closest('form')!);
    expect(await screen.findByRole('button', { name: 'Bob' })).toBeInTheDocument();

    expect(screen.queryAllByText('Alice 的私人攻略')).toHaveLength(0);
    fireEvent.change(screen.getByPlaceholderText('问攻略、版本或社区内容'), { target: { value: 'Bob 的问题' } });
    fireEvent.click(screen.getByRole('button', { name: '发送' }));
    await waitFor(() => expect(api.streamChatMessage).toHaveBeenCalledTimes(2));
    expect(api.streamChatMessage.mock.calls[1][0].sessionId).not.toBe(aliceSessionId);
  });

  it('keeps a completed login when the initial identity lookup finishes later', async () => {
    let resolveMe!: (value: null) => void;
    api.getMe.mockReturnValue(new Promise<null>((resolve) => { resolveMe = resolve; }));
    api.login.mockResolvedValue({ id: '2', username: 'bob', displayName: 'Bob', role: 'user' });
    render(<App />);

    fireEvent.click(screen.getByRole('button', { name: '登录 / 注册' }));
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'bob' } });
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'password123' } });
    fireEvent.submit(screen.getByLabelText('用户名').closest('form')!);
    expect(await screen.findByRole('button', { name: 'Bob' })).toBeInTheDocument();

    await act(async () => resolveMe(null));

    expect(screen.getByRole('button', { name: 'Bob' })).toBeInTheDocument();
  });

  it('keeps the latest explicit login when an earlier login finishes later', async () => {
    let resolveAlice!: (value: { id: string; username: string; displayName: string; role: string }) => void;
    let resolveBob!: (value: { id: string; username: string; displayName: string; role: string }) => void;
    api.login
      .mockReturnValueOnce(new Promise((resolve) => { resolveAlice = resolve; }))
      .mockReturnValueOnce(new Promise((resolve) => { resolveBob = resolve; }));
    render(<App />);

    fireEvent.click(screen.getByRole('button', { name: '登录 / 注册' }));
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'alice' } });
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'password123' } });
    fireEvent.submit(screen.getByLabelText('用户名').closest('form')!);
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'bob' } });
    fireEvent.submit(screen.getByLabelText('用户名').closest('form')!);

    await act(async () => resolveBob({ id: '2', username: 'bob', displayName: 'Bob', role: 'user' }));
    expect(await screen.findByRole('button', { name: 'Bob' })).toBeInTheDocument();

    await act(async () => resolveAlice({ id: '1', username: 'alice', displayName: 'Alice', role: 'user' }));
    expect(screen.getByRole('button', { name: 'Bob' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Alice' })).not.toBeInTheDocument();
  });

  it('keeps the latest catalog search when the initial load finishes later', async () => {
    let resolveInitial!: (value: unknown[]) => void;
    let resolveSearch!: (value: unknown[]) => void;
    api.listGames
      .mockReturnValueOnce(new Promise((resolve) => { resolveInitial = resolve; }))
      .mockReturnValueOnce(new Promise((resolve) => { resolveSearch = resolve; }));
    render(<App />);

    fireEvent.click(screen.getByRole('button', { name: '游戏库' }));
    fireEvent.change(screen.getByLabelText('搜索游戏'), { target: { value: 'new' } });
    fireEvent.click(screen.getByRole('button', { name: '搜索' }));
    await waitFor(() => expect(api.listGames).toHaveBeenCalledTimes(2));

    await act(async () => resolveSearch([{ id: '2', slug: 'new', name: 'New Game', summary: '', owned: false }]));
    expect(await screen.findByText('New Game')).toBeInTheDocument();

    await act(async () => resolveInitial([{ id: '1', slug: 'old', name: 'Old Game', summary: '', owned: false }]));
    expect(screen.getByText('New Game')).toBeInTheDocument();
    expect(screen.queryByText('Old Game')).not.toBeInTheDocument();
  });

  it('does not restore an abandoned game detail after returning to the catalog', async () => {
    const game = { id: '1', slug: 'old', name: 'Old Game', summary: 'Old summary', owned: false };
    let resolveGame!: (value: unknown) => void;
    api.listGames.mockResolvedValue([game]);
    api.getGame.mockReturnValue(new Promise((resolve) => { resolveGame = resolve; }));
    render(<App />);

    fireEvent.click(screen.getByRole('button', { name: '游戏库' }));
    fireEvent.click(await screen.findByRole('button', { name: /Old Game/ }));
    fireEvent.click(screen.getByRole('button', { name: '游戏助手' }));
    await act(async () => resolveGame({ ...game, description: 'Loaded detail' }));

    fireEvent.click(screen.getByRole('button', { name: '游戏库' }));
    expect(await screen.findByRole('button', { name: /Old Game/ })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Old Game', level: 1 })).not.toBeInTheDocument();
  });
});
