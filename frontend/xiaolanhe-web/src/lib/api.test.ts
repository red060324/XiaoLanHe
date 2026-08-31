import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  claimCoupon,
  createCommunityPost,
  createOrder,
  getMe,
  listCommunityPosts,
  listCouponClaims,
  listDeals,
  listGames,
  listOrders,
  logout,
  payOrder,
  sendChatMessage,
  setCommunityReaction,
  streamChatMessage
} from './api';

const fetchMock = vi.fn();

beforeEach(() => {
  fetchMock.mockReset();
  vi.stubGlobal('fetch', fetchMock);
});

describe('account API', () => {
  it('returns the authenticated user', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ user: { id: '1', username: 'player', displayName: 'Player', role: 'user' } }), { status: 200 }));
    await expect(getMe()).resolves.toMatchObject({ username: 'player' });
    expect(fetchMock).toHaveBeenCalledWith('/api/me', expect.objectContaining({ credentials: 'include' }));
  });

  it('does not disguise a dependency failure as an anonymous session', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ error: { message: 'authentication is unavailable' } }), { status: 503 }));
    await expect(getMe()).rejects.toThrow('authentication is unavailable');
  });

  it('reports logout failure instead of clearing local identity', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ error: { message: 'logout is unavailable' } }), { status: 503 }));
    await expect(logout()).rejects.toThrow('logout is unavailable');
  });
});

describe('catalog API', () => {
  it('encodes the query and returns games', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ items: [{ id: '1', slug: 'demo', name: 'Demo', summary: '', owned: false }] }), { status: 200 }));
    await expect(listGames(' demo game ')).resolves.toHaveLength(1);
    expect(fetchMock).toHaveBeenCalledWith('/api/games?query=demo+game', expect.objectContaining({ credentials: 'include' }));
  });
});

describe('assistant API', () => {
  it('surfaces the shared server error message for REST and SSE', async () => {
    const body = JSON.stringify({ error: { message: 'assistant unavailable' } });
    fetchMock.mockResolvedValueOnce(new Response(body, { status: 503 }));
    await expect(sendChatMessage({ message: 'help' })).rejects.toMatchObject({ message: 'assistant unavailable' });

    fetchMock.mockResolvedValueOnce(new Response(body, { status: 503 }));
    await expect(streamChatMessage({ message: 'help' }, vi.fn())).rejects.toMatchObject({ message: 'assistant unavailable' });
  });
});

describe('commerce API', () => {
  it('encodes deal and order cursors', async () => {
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ items: [], nextCursor: 'deal-next' }), { status: 200 }));
    await expect(listDeals('42', 'deal page')).resolves.toMatchObject({ nextCursor: 'deal-next' });
    expect(fetchMock).toHaveBeenLastCalledWith('/api/deals?gameId=42&cursor=deal+page', expect.objectContaining({ credentials: 'include' }));

    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ items: [], nextCursor: 'order-next' }), { status: 200 }));
    await expect(listOrders('order page')).resolves.toMatchObject({ nextCursor: 'order-next' });
    expect(fetchMock).toHaveBeenLastCalledWith('/api/orders?cursor=order+page', expect.objectContaining({ credentials: 'include' }));
  });

  it('loads available coupon claims with a cursor', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ items: [], nextCursor: 'claim-next' }), { status: 200 }));

    await expect(listCouponClaims('claim page')).resolves.toMatchObject({ nextCursor: 'claim-next' });
    expect(fetchMock).toHaveBeenCalledWith('/api/coupon-claims?cursor=claim+page', expect.objectContaining({ credentials: 'include' }));
  });

  it('sends idempotency keys without inventing request bodies', async () => {
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ claim: { id: '9' }, replayed: false }), { status: 201 }));
    await claimCoupon('WELCOME 20', 'claim-key.01');
    expect(fetchMock).toHaveBeenLastCalledWith('/api/coupons/WELCOME%2020/claims', expect.objectContaining({
      method: 'POST', headers: { 'Idempotency-Key': 'claim-key.01' }, credentials: 'include'
    }));

    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ order: { orderNo: 'ord_1' }, replayed: false }), { status: 200 }));
    await payOrder('ord/1', 'payment-key.01');
    expect(fetchMock).toHaveBeenLastCalledWith('/api/orders/ord%2F1/payments/sandbox', expect.objectContaining({
      method: 'POST', headers: { 'Idempotency-Key': 'payment-key.01' }, credentials: 'include'
    }));
  });

  it('creates an order from edition identity without a client price', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ order: { orderNo: 'ord_1' }, replayed: false }), { status: 201 }));
    await createOrder({ editionId: '12', region: 'GLOBAL', currency: 'USD', couponClaimId: '9' }, 'order-key.01');
    expect(fetchMock).toHaveBeenCalledWith('/api/orders', expect.objectContaining({
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Idempotency-Key': 'order-key.01' },
      body: JSON.stringify({ editionId: '12', region: 'GLOBAL', currency: 'USD', couponClaimId: '9' }),
      credentials: 'include'
    }));
  });
});

describe('community API', () => {
  it('encodes feed filters and returns a cursor page', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ items: [], nextCursor: 'next' }), { status: 200 }));

    await expect(listCommunityPosts('42', 'next page')).resolves.toEqual({ items: [], nextCursor: 'next' });
    expect(fetchMock).toHaveBeenCalledWith('/api/community/posts?gameId=42&cursor=next+page', expect.objectContaining({ credentials: 'include' }));
  });

  it('creates a post with JSON and returns the post payload', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ post: { id: '9', title: 'Guide' } }), { status: 201 }));

    await expect(createCommunityPost({ gameId: '42', title: 'Guide', content: 'Route' })).resolves.toMatchObject({ id: '9', title: 'Guide' });
    expect(fetchMock).toHaveBeenCalledWith('/api/community/posts', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ gameId: '42', title: 'Guide', content: 'Route' }),
      credentials: 'include'
    }));
  });

  it('uses the requested reaction operation', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ reactionCounts: { like: 1, helpful: 0, funny: 0 }, viewerReactions: ['like'] }), { status: 200 }));

    await setCommunityReaction('9', 'like', true);
    expect(fetchMock).toHaveBeenCalledWith('/api/community/posts/9/reactions/like', expect.objectContaining({ method: 'PUT', credentials: 'include' }));
  });
});
