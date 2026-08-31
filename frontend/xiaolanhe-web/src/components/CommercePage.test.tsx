import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import CommercePage from './CommercePage';
import { Deal, Game, Order, User } from '../lib/api';

const api = vi.hoisted(() => ({
  claimCoupon: vi.fn(),
  createOrder: vi.fn(),
  listDeals: vi.fn(),
  listOrders: vi.fn(),
  payOrder: vi.fn()
}));

vi.mock('../lib/api', () => api);

const user: User = { id: '7', username: 'player', displayName: 'Player', role: 'user' };
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

beforeEach(() => {
  api.listDeals.mockResolvedValue({ items: [] });
  api.listOrders.mockResolvedValue({ items: [] });
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('CommercePage', () => {
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

  it('reuses the checkout key after a recoverable error', async () => {
    api.createOrder.mockRejectedValueOnce(new Error('暂时不可用')).mockResolvedValueOnce({ order: pendingOrder, replayed: true });
    api.listOrders.mockResolvedValue({ items: [pendingOrder] });
    render(<CommercePage user={user} games={games} onRequireLogin={vi.fn()} onOwned={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: '购买' }));
    expect(await screen.findByText('暂时不可用')).toBeInTheDocument();
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
});
