import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
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
  listFlashSales: vi.fn(),
  listCouponClaims: vi.fn(),
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
  api.listFlashSales.mockResolvedValue({ items: [] });
  api.listCouponClaims.mockResolvedValue({ items: [] });
  api.listOrders.mockResolvedValue({ items: [] });
  api.streamChatMessage.mockResolvedValue(undefined);
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('App', () => {
  it('exposes every approved destination in responsive navigation', () => {
    render(<App />);

    const navigation = screen.getByRole('navigation', { name: '主要导航' });
    for (const name of ['发现游戏', '游戏社区', '优惠商店', '游戏助手', '我的订单', '账号']) {
      expect(within(navigation).getByRole('button', { name })).toBeInTheDocument();
    }
  });

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

  it('loads purchasable editions for catalog summaries', async () => {
    const summary = { id: '1', slug: 'demo', name: 'Demo Game', summary: 'Demo', owned: false };
    api.listGames.mockResolvedValue([summary]);
    api.getGame.mockResolvedValue({
      ...summary,
      editions: [{
        id: '12',
        code: 'standard',
        name: 'Standard',
        owned: false,
        price: { amountMinor: 1999, currency: 'USD', region: 'GLOBAL' }
      }]
    });
    render(<App />);

    fireEvent.click(screen.getByRole('button', { name: /优惠商店/ }));

    expect(await screen.findByRole('button', { name: '购买' })).toBeInTheDocument();
    expect(api.getGame).toHaveBeenCalledWith('demo');
  });

  it('keeps commerce loading failures inside the scrollable page stage', async () => {
    api.listGames.mockResolvedValue([{ id: '1', slug: 'demo', name: 'Demo Game', summary: 'Demo', owned: false }]);
    api.getGame.mockRejectedValueOnce(new Error('purchase details unavailable'));
    render(<App />);

    fireEvent.click(screen.getByRole('button', { name: '游戏库' }));
    await screen.findByRole('button', { name: /Demo Game/ });
    fireEvent.click(screen.getByRole('button', { name: '优惠商店' }));

    const error = await screen.findByText('purchase details unavailable');
    const pageStage = error.closest('.page-stage');
    const commerceHeading = screen.getByRole('heading', { name: '优惠与游戏' });
    expect(pageStage).not.toBeNull();
    expect(commerceHeading.closest('.page-stage')).toBe(pageStage);
    expect(pageStage?.querySelectorAll('.page-stage')).toHaveLength(0);
  });

  it('reuses the checkout idempotency key after leaving commerce and returning', async () => {
    const game = {
      id: '3',
      slug: 'demo',
      name: 'Demo',
      summary: '',
      owned: false,
      editions: [{
        id: '12',
        code: 'standard',
        name: 'Standard',
        owned: false,
        price: { amountMinor: 1999, currency: 'USD', region: 'GLOBAL' }
      }]
    };
    let rejectFirst!: (reason?: unknown) => void;
    api.getMe.mockResolvedValue({ id: '7', username: 'player', displayName: 'Player', role: 'user' });
    api.listGames.mockResolvedValue([game]);
    api.createOrder
      .mockReturnValueOnce(new Promise((_resolve, reject) => { rejectFirst = reject; }))
      .mockResolvedValueOnce({
        order: {
          orderNo: 'ord_0123456789abcdef0123456789abcdef',
          status: 'pending_payment',
          currency: 'USD',
          subtotalMinor: 1999,
          discountMinor: 0,
          totalMinor: 1999,
          item: {
            editionId: '12',
            gameSlug: 'demo',
            gameName: 'Demo',
            editionCode: 'standard',
            editionName: 'Standard',
            unitPriceMinor: 1999,
            region: 'GLOBAL'
          },
          createdAt: '2026-09-02T00:00:00Z',
          updatedAt: '2026-09-02T00:00:00Z'
        },
        replayed: true
      });

    render(<App />);

    await screen.findByRole('button', { name: 'Player' });
    fireEvent.click(screen.getByRole('button', { name: '优惠商店' }));
    fireEvent.click(await screen.findByRole('button', { name: '购买' }));
    await waitFor(() => expect(api.createOrder).toHaveBeenCalledOnce());
    const firstKey = api.createOrder.mock.calls[0][1];

    fireEvent.click(screen.getByRole('button', { name: '游戏社区' }));
    await screen.findByRole('heading', { name: '游戏社区' });
    await act(async () => rejectFirst(new Error('response lost after server commit')));

    fireEvent.click(screen.getByRole('button', { name: '优惠商店' }));
    fireEvent.click(await screen.findByRole('button', { name: '购买' }));

    await waitFor(() => expect(api.createOrder).toHaveBeenCalledTimes(2));
    expect(api.createOrder.mock.calls[1][1]).toBe(firstKey);
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

  it('keeps a newer stream cancellable after an earlier stream finishes', async () => {
    let rejectFirst!: (reason?: unknown) => void;
    let secondSignal: AbortSignal | undefined;
    api.streamChatMessage
      .mockImplementationOnce(() => new Promise<void>((_resolve, reject) => { rejectFirst = reject; }))
      .mockImplementationOnce((_payload, _onChunk, requestSignal: AbortSignal) => {
        secondSignal = requestSignal;
        return new Promise<void>((_resolve, reject) => requestSignal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError'))));
      });
    render(<App />);

    fireEvent.change(screen.getByPlaceholderText('问攻略、版本或社区内容'), { target: { value: '第一个问题' } });
    fireEvent.click(screen.getByRole('button', { name: '发送' }));
    await waitFor(() => expect(api.streamChatMessage).toHaveBeenCalledOnce());
    fireEvent.click(screen.getByRole('button', { name: '游戏助手' }));
    fireEvent.change(screen.getByPlaceholderText('问攻略、版本或社区内容'), { target: { value: '第二个问题' } });
    fireEvent.click(screen.getByRole('button', { name: '发送' }));
    await waitFor(() => expect(api.streamChatMessage).toHaveBeenCalledTimes(2));

    await act(async () => rejectFirst(new DOMException('aborted', 'AbortError')));

    fireEvent.click(screen.getByRole('button', { name: '停止' }));
    expect(secondSignal?.aborted).toBe(true);
    expect(await screen.findByText('已停止生成。')).toBeInTheDocument();
  });

  it('cancels the active stream and releases loading when history changes', async () => {
    const now = '2026-09-02T00:00:00.000Z';
    window.localStorage.setItem('xiaolanhe_local_conversations:guest', JSON.stringify([
      { id: 'f47ac10b-58cc-4372-a567-0e02b2c3d479', title: '第一段对话', messages: [], createdAt: now, updatedAt: now },
      { id: '110ec58a-a0f2-4ac4-8393-c866d813b8d1', title: '第二段对话', messages: [], createdAt: now, updatedAt: now }
    ]));
    let resolveMe!: (value: null) => void;
    api.getMe.mockReturnValue(new Promise<null>((resolve) => { resolveMe = resolve; }));
    let signal!: AbortSignal;
    api.streamChatMessage.mockImplementation((_payload, _onChunk, requestSignal: AbortSignal) => {
      signal = requestSignal;
      return new Promise<void>((_resolve, reject) => {
        requestSignal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
      });
    });

    render(<App />);
    await act(async () => resolveMe(null));
    fireEvent.change(screen.getByPlaceholderText('问攻略、版本或社区内容'), { target: { value: '仍在生成的问题' } });
    fireEvent.click(screen.getByRole('button', { name: '发送' }));
    await waitFor(() => expect(api.streamChatMessage).toHaveBeenCalledOnce());

    fireEvent.click(screen.getByRole('button', { name: '第二段对话' }));

    await waitFor(() => expect(signal.aborted).toBe(true));
    await waitFor(() => expect(screen.queryByRole('button', { name: '停止' })).not.toBeInTheDocument());
    expect(screen.getByRole('button', { name: '发送' })).toBeInTheDocument();
  });

  it('shows a failure message when the stream fails before replying', async () => {
    api.streamChatMessage.mockRejectedValue(new Error('assistant unavailable'));
    render(<App />);

    fireEvent.change(screen.getByPlaceholderText('问攻略、版本或社区内容'), { target: { value: '攻略' } });
    fireEvent.click(screen.getByRole('button', { name: '发送' }));

    expect(await screen.findByText('生成失败，请重试。')).toBeInTheDocument();
    expect(screen.getByRole('alert')).toHaveTextContent('请求失败：assistant unavailable');
  });

  it('preserves assistant content that legitimately starts with data:', async () => {
    api.streamChatMessage.mockImplementation(async (_payload, onChunk: (chunk: string) => void) => {
      onChunk('data: value');
      return {};
    });
    render(<App />);

    fireEvent.change(screen.getByPlaceholderText('问攻略、版本或社区内容'), { target: { value: 'show a data example' } });
    fireEvent.click(screen.getByRole('button', { name: '发送' }));

    await screen.findByRole('button', { name: '发送' });
    expect(await screen.findByText('data: value')).toBeInTheDocument();
  });

  it('clears an assistant failure when navigating to the catalog', async () => {
    api.streamChatMessage.mockRejectedValue(new Error('assistant unavailable'));
    render(<App />);

    fireEvent.change(screen.getByPlaceholderText('问攻略、版本或社区内容'), { target: { value: '攻略' } });
    fireEvent.click(screen.getByRole('button', { name: '发送' }));
    expect(await screen.findByText('请求失败：assistant unavailable')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '游戏库' }));

    expect(await screen.findByText('没有找到游戏。')).toBeInTheDocument();
    expect(screen.queryByText('assistant unavailable')).not.toBeInTheDocument();
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

  it('prevents repeated logout while the first request is pending', async () => {
    let resolveLogout!: () => void;
    const pendingLogout = new Promise<void>((resolve) => { resolveLogout = resolve; });
    api.getMe.mockResolvedValue({ id: '7', username: 'player', displayName: 'Player', role: 'user' });
    api.logout.mockReturnValue(pendingLogout);
    render(<App />);

    await screen.findByRole('button', { name: 'Player' });
    const logoutButton = screen.getByRole('button', { name: '退出登录' });
    fireEvent.click(logoutButton);
    fireEvent.click(logoutButton);

    expect(api.logout).toHaveBeenCalledOnce();
    expect(logoutButton).toBeDisabled();

    await act(async () => resolveLogout());
  });

  it('serializes cookie-mutating authentication requests', async () => {
    let resolveLogin!: (value: { id: string; username: string; displayName: string; role: string }) => void;
    api.login.mockReturnValue(new Promise((resolve) => { resolveLogin = resolve; }));
    render(<App />);

    fireEvent.click(screen.getByRole('button', { name: '登录 / 注册' }));
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'alice' } });
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'password123' } });
    const form = screen.getByLabelText('用户名').closest('form')!;
    fireEvent.submit(form);
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'bob' } });
    fireEvent.submit(form);

    expect(api.login).toHaveBeenCalledOnce();
    expect(screen.getByRole('button', { name: '登录' })).toBeDisabled();

    await act(async () => resolveLogin({ id: '1', username: 'alice', displayName: 'Alice', role: 'user' }));
    expect(await screen.findByRole('button', { name: 'Alice' })).toBeInTheDocument();
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
