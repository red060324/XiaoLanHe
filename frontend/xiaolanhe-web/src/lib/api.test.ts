import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createCommunityPost, getMe, listCommunityPosts, logout, setCommunityReaction, listGames } from './api';

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
