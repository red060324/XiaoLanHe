import { beforeEach, describe, expect, it, vi } from 'vitest';
import { getMe, listGames, logout } from './api';

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
