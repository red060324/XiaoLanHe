import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import CommercePage from './CommercePage';
import { Deal, FlashSale, FlashSaleRequest, Game, Order, User } from '../lib/api';

const api = vi.hoisted(() => ({
  claimCoupon: vi.fn(),
  createOrder: vi.fn(),
  getFlashSaleRequest: vi.fn(),
  listDeals: vi.fn(),
  listFlashSales: vi.fn(),
  listCouponClaims: vi.fn(),
  listOrders: vi.fn(),
  payOrder: vi.fn(),
  reserveFlashSale: vi.fn()
}));

vi.mock('../lib/api', () => api);

const user: User = { id: '7', username: 'player', displayName: 'Player', role: 'user' };
const otherUser: User = { id: '8', username: 'other', displayName: 'Other', role: 'user' };
const games: Game[] = [{
  id: '3', slug: 'demo', name: 'Demo', summary: '', owned: false,
  editions: [{ id: '12', code: 'standard', name: 'Standard', owned: false, price: { amountMinor: 1999, currency: 'USD', region: 'GLOBAL' } }]
}];
const deal: Deal = {
  id: '4', code: 'WELCOME20', name: 'Welcome', discountType: 'percentage', percentageBps: 2000,
  currency: 'USD', minimumMinor: 1000, remainingStock: 2, perUserLimit: 1,
  startsAt: '2026-08-31T00:00:00Z', endsAt: '2026-09-30T00:00:00Z', viewerClaimCount: 0
};
const pendingOrder: Order = {
  orderNo: 'ord_0123456789abcdef0123456789abcdef', status: 'pending_payment', currency: 'USD',
  subtotalMinor: 1999, discountMinor: 399, totalMinor: 1600, couponClaimId: '9',
  item: { editionId: '12', gameSlug: 'demo', gameName: 'Demo', editionCode: 'standard', editionName: 'Standard', unitPriceMinor: 1999, region: 'GLOBAL' },
  createdAt: '2026-08-31T08:00:00Z', updatedAt: '2026-08-31T08:00:00Z'
};
const flashSale: FlashSale = {
  id: '41', code: 'AUTUMN-DEMO', gameSlug: 'demo', gameName: 'Demo', editionId: '12', editionName: 'Standard',
  region: 'GLOBAL', currency: 'USD', salePriceMinor: 999, status: 'active', startsAt: '2026-09-02T00:00:00Z',
  endsAt: '2026-09-30T00:00:00Z', availability: 'available'
};
const queuedFlashRequest: FlashSaleRequest = {
  requestId: 'fsr_15_0123456789abcdef0123456789abcdef', activityId: '41', status: 'queued', orderNo: '', failureCode: '', paymentExpiresAt: ''
};

beforeEach(() => {
  api.listDeals.mockResolvedValue({ items: [] });
  api.listFlashSales.mockResolvedValue({ items: [] });
  api.listCouponClaims.mockResolvedValue({ items: [] });
  api.listOrders.mockResolvedValue({ items: [] });
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('CommercePage', () => {
  it('reserves once, polls asynchronously, and opens the created order', async () => {
    api.listFlashSales.mockResolvedValue({ items: [flashSale] });
    api.reserveFlashSale.mockResolvedValue({ request: queuedFlashRequest, replayed: false });
    api.getFlashSaleRequest.mockResolvedValue({ ...queuedFlashRequest, status: 'order_ready', orderNo: pendingOrder.orderNo });
    api.listOrders.mockResolvedValue({ items: [pendingOrder] });
    render(<CommercePage user={user} games={games} onRequireLogin={vi.fn()} onOwned={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: '立即抢购' }));

    await waitFor(() => expect(api.reserveFlashSale).toHaveBeenCalledWith('41', expect.stringMatching(/^flash-sale:/)));
    expect(await screen.findByText('抢购成功，订单已创建')).toBeInTheDocument();
    expect(api.getFlashSaleRequest).toHaveBeenCalledWith(queuedFlashRequest.requestId, expect.any(AbortSignal));
    fireEvent.click(screen.getByRole('button', { name: '查看订单' }));
    expect(await screen.findByText(pendingOrder.orderNo)).toBeInTheDocument();
  });

  it('reuses the flash-sale key after a recoverable reservation error', async () => {
    api.listFlashSales.mockResolvedValue({ items: [flashSale] });
    api.reserveFlashSale
      .mockRejectedValueOnce(new Error('抢购服务繁忙'))
      .mockResolvedValueOnce({ request: { ...queuedFlashRequest, status: 'failed', failureCode: 'final_stock_exhausted' }, replayed: true });
    render(<CommercePage user={user} games={games} onRequireLogin={vi.fn()} onOwned={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: '立即抢购' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('抢购服务繁忙');
    fireEvent.click(screen.getByRole('button', { name: '立即抢购' }));

    await waitFor(() => expect(api.reserveFlashSale).toHaveBeenCalledTimes(2));
    expect(api.reserveFlashSale.mock.calls[1][1]).toBe(api.reserveFlashSale.mock.calls[0][1]);
    expect(await screen.findByText('抢购未完成，库存将自动释放')).toBeInTheDocument();
  });

  it('cancels flash-sale polling when the signed-in user changes', async () => {
    api.listFlashSales.mockResolvedValue({ items: [flashSale] });
    api.reserveFlashSale.mockResolvedValue({ request: queuedFlashRequest, replayed: false });
    api.getFlashSaleRequest.mockImplementation((_requestId: string, signal: AbortSignal) => new Promise((_resolve, reject) => {
      signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true });
    }));
    const view = render(<CommercePage user={user} games={games} onRequireLogin={vi.fn()} onOwned={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: '立即抢购' }));
    await waitFor(() => expect(api.getFlashSaleRequest).toHaveBeenCalledOnce());
    const signal = api.getFlashSaleRequest.mock.calls[0][1] as AbortSignal;
    view.rerender(<CommercePage user={otherUser} games={games} onRequireLogin={vi.fn()} onOwned={vi.fn()} />);

    expect(signal.aborted).toBe(true);
    expect(screen.queryByText('抢购成功，订单已创建')).not.toBeInTheDocument();
    expect(screen.queryByText('已排队，等待订单服务处理')).not.toBeInTheDocument();
  });

  it('exposes the selected commerce view to assistive technology', async () => {
    render(<CommercePage user={user} games={games} onRequireLogin={vi.fn()} onOwned={vi.fn()} />);

    const deals = screen.getByRole('button', { name: '优惠与购买' });
    const orders = screen.getByRole('button', { name: '我的订单' });
    expect(deals).toHaveAttribute('aria-pressed', 'true');
    expect(orders).toHaveAttribute('aria-pressed', 'false');

    fireEvent.click(orders);

    expect(deals).toHaveAttribute('aria-pressed', 'false');
    expect(orders).toHaveAttribute('aria-pressed', 'true');
  });

  it('requires login before claiming a public deal', async () => {
    api.listDeals.mockResolvedValue({ items: [deal] });
    const onRequireLogin = vi.fn();
    render(<CommercePage user={null} games={games} onRequireLogin={onRequireLogin} onOwned={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: '领取优惠券' }));
    expect(onRequireLogin).toHaveBeenCalledOnce();
    expect(api.claimCoupon).not.toHaveBeenCalled();
  });

  it('applies a newly claimed coupon to checkout', async () => {
    api.listDeals.mockResolvedValue({ items: [deal] });
    api.claimCoupon.mockResolvedValue({ claim: { id: '9', couponCode: 'WELCOME20', status: 'claimed', claimedAt: '2026-08-31T08:00:00Z' }, replayed: false });
    api.createOrder.mockResolvedValue({ order: pendingOrder, replayed: false });
    api.listOrders.mockResolvedValue({ items: [pendingOrder] });
    render(<CommercePage user={user} games={games} onRequireLogin={vi.fn()} onOwned={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: '领取优惠券' }));
    await waitFor(() => expect(screen.getByLabelText('本次结算优惠券')).toHaveValue('9'));
    fireEvent.click(screen.getByRole('button', { name: '购买' }));

    await waitFor(() => expect(api.createOrder).toHaveBeenCalledWith({ editionId: '12', region: 'GLOBAL', currency: 'USD', couponClaimId: '9' }, expect.stringMatching(/^order:/)));
    expect(await screen.findByText('待支付')).toBeInTheDocument();
  });

  it('preserves a newer coupon selection when an earlier checkout completes', async () => {
    const nextClaim = { id: '10', couponCode: 'NEXT10', status: 'claimed', claimedAt: '2026-08-31T08:01:00Z' };
    let resolveOrder!: (value: unknown) => void;
    api.listCouponClaims.mockResolvedValue({
      items: [
        { id: '9', couponCode: 'WELCOME20', status: 'claimed', claimedAt: '2026-08-31T08:00:00Z' },
        nextClaim
      ]
    });
    api.createOrder.mockReturnValue(new Promise((resolve) => { resolveOrder = resolve; }));
    api.listOrders.mockResolvedValue({ items: [pendingOrder] });
    render(<CommercePage user={user} games={games} onRequireLogin={vi.fn()} onOwned={vi.fn()} />);

    await screen.findByRole('option', { name: 'NEXT10 · #10' });
    fireEvent.change(screen.getByLabelText('本次结算优惠券'), { target: { value: '9' } });
    fireEvent.click(screen.getByRole('button', { name: '购买' }));
    await waitFor(() => expect(api.createOrder).toHaveBeenCalledOnce());
    fireEvent.change(screen.getByLabelText('本次结算优惠券'), { target: { value: nextClaim.id } });

    await act(async () => resolveOrder({ order: pendingOrder, replayed: false }));
    fireEvent.click(await screen.findByRole('button', { name: '优惠与购买' }));

    expect(screen.getByLabelText('本次结算优惠券')).toHaveValue(nextClaim.id);
  });

  it('keeps the current game filter after an earlier coupon claim completes', async () => {
    const filteredDeal = { ...deal, id: '5', code: 'DEMO20', name: 'Demo only' };
    let resolveClaim!: (value: unknown) => void;
    api.listDeals
      .mockResolvedValueOnce({ items: [deal] })
      .mockResolvedValueOnce({ items: [filteredDeal] })
      .mockResolvedValueOnce({ items: [deal] });
    api.claimCoupon.mockReturnValue(new Promise((resolve) => { resolveClaim = resolve; }));
    render(<CommercePage user={user} games={games} onRequireLogin={vi.fn()} onOwned={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: '领取优惠券' }));
    await waitFor(() => expect(api.claimCoupon).toHaveBeenCalledOnce());
    fireEvent.change(screen.getByLabelText('适用游戏'), { target: { value: '3' } });
    expect(await screen.findByText('Demo only')).toBeInTheDocument();

    await act(async () => resolveClaim({
      claim: { id: '9', couponCode: deal.code, status: 'claimed', claimedAt: '2026-08-31T08:00:00Z' },
      replayed: false
    }));

    expect(screen.getByLabelText('适用游戏')).toHaveValue('3');
    expect(screen.getByText('Demo only')).toBeInTheDocument();
    expect(screen.queryByText(deal.name)).not.toBeInTheDocument();
  });

  it('restores an unredeemed coupon claim after remount', async () => {
    api.listDeals.mockResolvedValue({ items: [{ ...deal, viewerClaimCount: 1 }] });
    api.listCouponClaims.mockResolvedValue({ items: [{ id: '9', couponCode: 'WELCOME20', status: 'claimed', claimedAt: '2026-08-31T08:00:00Z' }] });
    api.createOrder.mockResolvedValue({ order: pendingOrder, replayed: false });
    api.listOrders.mockResolvedValue({ items: [pendingOrder] });
    render(<CommercePage user={user} games={games} onRequireLogin={vi.fn()} onOwned={vi.fn()} />);

    expect(await screen.findByRole('option', { name: 'WELCOME20 · #9' })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('本次结算优惠券'), { target: { value: '9' } });
    fireEvent.click(screen.getByRole('button', { name: '购买' }));

    await waitFor(() => expect(api.createOrder).toHaveBeenCalledWith({ editionId: '12', region: 'GLOBAL', currency: 'USD', couponClaimId: '9' }, expect.stringMatching(/^order:/)));
    fireEvent.click(screen.getByRole('button', { name: '优惠与购买' }));
    expect(screen.queryByRole('option', { name: 'WELCOME20 · #9' })).not.toBeInTheDocument();
  });

  it('reuses the checkout key after a recoverable error', async () => {
    api.createOrder.mockRejectedValueOnce(new Error('暂时不可用')).mockResolvedValueOnce({ order: pendingOrder, replayed: true });
    api.listOrders.mockResolvedValue({ items: [pendingOrder] });
    render(<CommercePage user={user} games={games} onRequireLogin={vi.fn()} onOwned={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: '购买' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('暂时不可用');
    fireEvent.click(screen.getByRole('button', { name: '购买' }));
    await waitFor(() => expect(api.createOrder).toHaveBeenCalledTimes(2));
    expect(api.createOrder.mock.calls[1][1]).toBe(api.createOrder.mock.calls[0][1]);
  });

  it('keeps unowned editions purchasable after owning another edition', async () => {
    const mixedOwnershipGames = [{
      ...games[0],
      owned: true,
      editions: [
        { ...games[0].editions![0], owned: true },
        { id: '13', code: 'deluxe', name: 'Deluxe', owned: false, price: { amountMinor: 2999, currency: 'USD', region: 'GLOBAL' } }
      ]
    }] as Game[];

    render(<CommercePage user={user} games={mixedOwnershipGames} onRequireLogin={vi.fn()} onOwned={vi.fn()} />);

    expect(await screen.findByRole('button', { name: '购买' })).toBeEnabled();
    expect(screen.getByRole('button', { name: '已拥有' })).toBeDisabled();
  });

  it('keeps a completed payment when an older history request finishes later', async () => {
    let resolveHistory!: (value: { items: Order[] }) => void;
    api.listOrders.mockReturnValue(new Promise((resolve) => { resolveHistory = resolve; }));
    api.createOrder.mockResolvedValue({ order: pendingOrder, replayed: false });
    api.payOrder.mockResolvedValue({ order: { ...pendingOrder, status: 'paid', payment: { provider: 'sandbox', reference: 'sandbox:ord', status: 'paid', amountMinor: 1600, createdAt: pendingOrder.updatedAt } }, replayed: false });
    const onOwned = vi.fn();
    render(<CommercePage user={user} games={games} onRequireLogin={vi.fn()} onOwned={onOwned} />);

    fireEvent.click(await screen.findByRole('button', { name: '购买' }));
    fireEvent.click(await screen.findByRole('button', { name: '沙箱支付' }));
    expect(await screen.findByText('已拥有')).toBeInTheDocument();
    expect(onOwned).toHaveBeenCalledOnce();

    await act(async () => resolveHistory({ items: [pendingOrder] }));
    expect(screen.getByText('已拥有')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '沙箱支付' })).not.toBeInTheDocument();
  });

  it('ignores an older history error after payment succeeds', async () => {
    let rejectHistory!: (reason?: unknown) => void;
    api.listOrders.mockReturnValue(new Promise((_resolve, reject) => { rejectHistory = reject; }));
    api.createOrder.mockResolvedValue({ order: pendingOrder, replayed: false });
    api.payOrder.mockResolvedValue({ order: { ...pendingOrder, status: 'paid', payment: { provider: 'sandbox', reference: 'sandbox:ord', status: 'paid', amountMinor: 1600, createdAt: pendingOrder.updatedAt } }, replayed: false });
    render(<CommercePage user={user} games={games} onRequireLogin={vi.fn()} onOwned={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: '购买' }));
    fireEvent.click(await screen.findByRole('button', { name: '沙箱支付' }));
    expect(await screen.findByText('已拥有')).toBeInTheDocument();

    await act(async () => rejectHistory(new Error('旧订单请求失败')));
    expect(screen.queryByText('旧订单请求失败')).not.toBeInTheDocument();
  });

  it('clears an order history error when returning to deals', async () => {
    api.listDeals.mockResolvedValue({ items: [deal] });
    api.listOrders.mockRejectedValue(new Error('订单服务暂不可用'));
    render(<CommercePage user={user} games={games} onRequireLogin={vi.fn()} onOwned={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: '我的订单' }));
    expect(await screen.findByText('订单服务暂不可用')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '优惠与购买' }));

    expect(screen.getByText(deal.name)).toBeInTheDocument();
    expect(screen.queryByText('订单服务暂不可用')).not.toBeInTheDocument();
  });

  it('ignores an order history error after returning to deals', async () => {
    let rejectHistory!: (reason?: unknown) => void;
    api.listDeals.mockResolvedValue({ items: [deal] });
    api.listOrders.mockReturnValue(new Promise((_resolve, reject) => { rejectHistory = reject; }));
    render(<CommercePage user={user} games={games} onRequireLogin={vi.fn()} onOwned={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: '我的订单' }));
    await waitFor(() => expect(api.listOrders).toHaveBeenCalledOnce());
    fireEvent.click(screen.getByRole('button', { name: '优惠与购买' }));
    await act(async () => rejectHistory(new Error('废弃订单请求失败')));

    expect(screen.getByText(deal.name)).toBeInTheDocument();
    expect(screen.queryByText('废弃订单请求失败')).not.toBeInTheDocument();
  });

  it('reloads viewer-specific deals when the signed-in user changes', async () => {
    api.listDeals
      .mockResolvedValueOnce({ items: [{ ...deal, viewerClaimCount: 1 }] })
      .mockResolvedValueOnce({ items: [deal] });
    const view = render(<CommercePage user={user} games={games} onRequireLogin={vi.fn()} onOwned={vi.fn()} />);

    expect(await screen.findByRole('button', { name: '已领取' })).toBeDisabled();
    view.rerender(<CommercePage user={otherUser} games={games} onRequireLogin={vi.fn()} onOwned={vi.fn()} />);

    await waitFor(() => expect(api.listDeals).toHaveBeenCalledTimes(2));
    expect(await screen.findByRole('button', { name: '领取优惠券' })).toBeEnabled();
  });

  it('clears the previous user order history while the next user loads', async () => {
    let resolveOtherHistory!: (value: { items: Order[] }) => void;
    api.listOrders
      .mockResolvedValueOnce({ items: [pendingOrder] })
      .mockReturnValueOnce(new Promise((resolve) => { resolveOtherHistory = resolve; }));
    const view = render(<CommercePage user={user} games={games} onRequireLogin={vi.fn()} onOwned={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: '我的订单' }));
    expect(await screen.findByText(pendingOrder.orderNo)).toBeInTheDocument();

    view.rerender(<CommercePage user={otherUser} games={games} onRequireLogin={vi.fn()} onOwned={vi.fn()} />);
    await waitFor(() => expect(api.listOrders).toHaveBeenCalledTimes(2));

    expect(screen.queryByText(pendingOrder.orderNo)).not.toBeInTheDocument();
    await act(async () => resolveOtherHistory({ items: [] }));
  });

  it('ignores a deal mutation error after switching to the orders tab', async () => {
    let rejectClaim!: (reason?: unknown) => void;
    api.listDeals.mockResolvedValue({ items: [deal] });
    api.claimCoupon.mockReturnValue(new Promise((_resolve, reject) => { rejectClaim = reject; }));
    render(<CommercePage user={user} games={games} onRequireLogin={vi.fn()} onOwned={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: '领取优惠券' }));
    await waitFor(() => expect(api.claimCoupon).toHaveBeenCalledOnce());
    fireEvent.click(screen.getByRole('button', { name: '我的订单' }));
    await act(async () => rejectClaim(new Error('旧优惠请求失败')));

    expect(screen.getByRole('button', { name: '我的订单' })).toHaveClass('active');
    expect(screen.queryByText('旧优惠请求失败')).not.toBeInTheDocument();
  });

  it('makes a claim selectable when it succeeds after switching tabs', async () => {
    const claimedCoupon = {
      id: '9',
      couponCode: 'WELCOME20',
      status: 'claimed' as const,
      claimedAt: '2026-08-31T08:00:00Z'
    };
    let resolveClaim!: (value: { claim: typeof claimedCoupon; replayed: boolean }) => void;
    api.listDeals.mockResolvedValue({ items: [deal] });
    api.listCouponClaims
      .mockResolvedValueOnce({ items: [] })
      .mockResolvedValue({ items: [claimedCoupon] });
    api.claimCoupon.mockReturnValue(new Promise((resolve) => { resolveClaim = resolve; }));
    render(<CommercePage user={user} games={games} onRequireLogin={vi.fn()} onOwned={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: '领取优惠券' }));
    await waitFor(() => expect(api.claimCoupon).toHaveBeenCalledOnce());
    fireEvent.click(screen.getByRole('button', { name: '我的订单' }));

    await act(async () => resolveClaim({ claim: claimedCoupon, replayed: false }));
    fireEvent.click(screen.getByRole('button', { name: '优惠与购买' }));

    const selector = screen.getByLabelText('本次结算优惠券');
    await screen.findByRole('option', { name: 'WELCOME20 · #9' });
    fireEvent.change(selector, { target: { value: '9' } });
    expect(selector).toHaveValue('9');
  });
});
