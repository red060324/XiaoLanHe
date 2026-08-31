const DEFAULT_API_BASE_URL = '';

export type ChatMessageRequest = {
  sessionId?: string;
  message: string;
};

export type ChatMessageResponse = {
  sessionId: string;
  answer: string;
  createdAt: string;
};

export type User = {
  id: string;
  username: string;
  displayName: string;
  role: 'user' | 'admin';
};

export type Price = {
  amountMinor: number;
  currency: string;
  region: string;
};

export type Game = {
  id: string;
  slug: string;
  name: string;
  summary: string;
  description?: string;
  developer?: string;
  publisher?: string;
  releaseDate?: string;
  coverUrl?: string;
  owned: boolean;
  editions?: Array<{ id: string; code: string; name: string; description?: string; owned: boolean; price?: Price }>;
};

export type Deal = {
  id: string;
  code: string;
  name: string;
  discountType: 'fixed' | 'percentage';
  fixedMinor?: number;
  percentageBps?: number;
  currency: string;
  minimumMinor: number;
  remainingStock: number;
  perUserLimit: number;
  gameId?: string;
  editionId?: string;
  startsAt: string;
  endsAt: string;
  viewerClaimCount: number;
};

export type CouponClaim = {
  id: string;
  couponCode: string;
  status: 'claimed' | 'redeemed' | 'expired';
  claimedAt: string;
};

export type Order = {
  orderNo: string;
  status: 'pending_payment' | 'paid' | 'cancelled' | 'expired';
  currency: string;
  subtotalMinor: number;
  discountMinor: number;
  totalMinor: number;
  couponClaimId?: string;
  item: {
    editionId: string;
    gameSlug: string;
    gameName: string;
    editionCode: string;
    editionName: string;
    unitPriceMinor: number;
    region: string;
  };
  payment?: {
    provider: string;
    reference: string;
    status: string;
    amountMinor: number;
    createdAt: string;
  };
  createdAt: string;
  updatedAt: string;
};

export type CommunityAuthor = {
  id: string;
  username: string;
  displayName: string;
};

export type CommunityPost = {
  id: string;
  title: string;
  content: string;
  status: 'published' | 'hidden';
  author: CommunityAuthor;
  game?: { id: string; slug: string; name: string };
  commentCount: number;
  reactionCounts: Record<'like' | 'helpful' | 'funny', number>;
  viewerReactions: Array<'like' | 'helpful' | 'funny'>;
  createdAt: string;
  updatedAt: string;
};

export type CommunityComment = {
  id: string;
  postId: string;
  content: string;
  status: 'published' | 'hidden';
  author: CommunityAuthor;
  createdAt: string;
  updatedAt: string;
};

export type Page<T> = { items: T[]; nextCursor?: string };

function resolveApiBaseUrl(): string {
  const configured = import.meta.env.VITE_API_BASE_URL;
  return configured && configured.trim().length > 0 ? configured : DEFAULT_API_BASE_URL;
}

async function requestJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await request(path, init);
  return response.json() as Promise<T>;
}

class APIError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
  }
}

async function request(path: string, init?: RequestInit): Promise<Response> {
  const response = await fetch(`${resolveApiBaseUrl()}${path}`, { credentials: 'include', ...init });
  if (!response.ok) {
    const body = (await response.json().catch(() => null)) as { error?: { message?: string } } | null;
    throw new APIError(response.status, body?.error?.message ?? `请求失败（${response.status}）`);
  }
  return response;
}

export async function getMe(): Promise<User | null> {
  try {
    const result = await requestJSON<{ user: User }>('/api/me');
    return result.user;
  } catch (error) {
    if (error instanceof APIError && error.status === 401) return null;
    throw error;
  }
}

export async function login(username: string, password: string): Promise<User> {
  const result = await requestJSON<{ user: User }>('/api/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password })
  });
  return result.user;
}

export async function register(username: string, displayName: string, password: string): Promise<User> {
  const result = await requestJSON<{ user: User }>('/api/auth/register', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, displayName, password })
  });
  return result.user;
}

export async function logout(): Promise<void> {
  await request('/api/auth/logout', { method: 'POST' });
}

export async function listGames(query = ''): Promise<Game[]> {
  const params = new URLSearchParams();
  if (query.trim()) params.set('query', query.trim());
  const result = await requestJSON<{ items: Game[] }>(`/api/games?${params}`);
  return result.items;
}

export async function getGame(slug: string): Promise<Game> {
  const result = await requestJSON<{ game: Game }>(`/api/games/${encodeURIComponent(slug)}`);
  return result.game;
}

export async function listDeals(gameId = '', cursor = ''): Promise<Page<Deal>> {
  const params = new URLSearchParams();
  if (gameId) params.set('gameId', gameId);
  if (cursor) params.set('cursor', cursor);
  return requestJSON<Page<Deal>>(`/api/deals?${params}`);
}

export async function claimCoupon(code: string, idempotencyKey: string): Promise<{ claim: CouponClaim; replayed: boolean }> {
  return requestJSON<{ claim: CouponClaim; replayed: boolean }>(`/api/coupons/${encodeURIComponent(code)}/claims`, {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey }
  });
}

export async function createOrder(input: { editionId: string; region: string; currency: string; couponClaimId?: string }, idempotencyKey: string): Promise<{ order: Order; replayed: boolean }> {
  return requestJSON<{ order: Order; replayed: boolean }>('/api/orders', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(input)
  });
}

export async function listOrders(cursor = ''): Promise<Page<Order>> {
  const params = new URLSearchParams();
  if (cursor) params.set('cursor', cursor);
  return requestJSON<Page<Order>>(`/api/orders?${params}`);
}

export async function payOrder(orderNo: string, idempotencyKey: string): Promise<{ order: Order; replayed: boolean }> {
  return requestJSON<{ order: Order; replayed: boolean }>(`/api/orders/${encodeURIComponent(orderNo)}/payments/sandbox`, {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey }
  });
}

export async function listCommunityPosts(gameId = '', cursor = ''): Promise<Page<CommunityPost>> {
  const params = new URLSearchParams();
  if (gameId) params.set('gameId', gameId);
  if (cursor) params.set('cursor', cursor);
  return requestJSON<Page<CommunityPost>>(`/api/community/posts?${params}`);
}

export async function getCommunityPost(id: string): Promise<CommunityPost> {
  const result = await requestJSON<{ post: CommunityPost }>(`/api/community/posts/${encodeURIComponent(id)}`);
  return result.post;
}

export async function createCommunityPost(input: { gameId?: string; title: string; content: string }): Promise<CommunityPost> {
  const result = await requestJSON<{ post: CommunityPost }>('/api/community/posts', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input)
  });
  return result.post;
}

export async function updateCommunityPost(id: string, input: { gameId?: string; title: string; content: string }): Promise<CommunityPost> {
  const result = await requestJSON<{ post: CommunityPost }>(`/api/community/posts/${encodeURIComponent(id)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input)
  });
  return result.post;
}

export async function deleteCommunityPost(id: string): Promise<void> {
  await request(`/api/community/posts/${encodeURIComponent(id)}`, { method: 'DELETE' });
}

export async function listCommunityComments(postId: string, cursor = ''): Promise<Page<CommunityComment>> {
  const params = new URLSearchParams();
  if (cursor) params.set('cursor', cursor);
  return requestJSON<Page<CommunityComment>>(`/api/community/posts/${encodeURIComponent(postId)}/comments?${params}`);
}

export async function createCommunityComment(postId: string, content: string): Promise<CommunityComment> {
  const result = await requestJSON<{ comment: CommunityComment }>(`/api/community/posts/${encodeURIComponent(postId)}/comments`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content })
  });
  return result.comment;
}

export async function updateCommunityComment(id: string, content: string): Promise<CommunityComment> {
  const result = await requestJSON<{ comment: CommunityComment }>(`/api/community/comments/${encodeURIComponent(id)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content })
  });
  return result.comment;
}

export async function deleteCommunityComment(id: string): Promise<void> {
  await request(`/api/community/comments/${encodeURIComponent(id)}`, { method: 'DELETE' });
}

export async function setCommunityReaction(postId: string, type: 'like' | 'helpful' | 'funny', active: boolean): Promise<Pick<CommunityPost, 'reactionCounts' | 'viewerReactions'>> {
  return requestJSON<Pick<CommunityPost, 'reactionCounts' | 'viewerReactions'>>(`/api/community/posts/${encodeURIComponent(postId)}/reactions/${type}`, { method: active ? 'PUT' : 'DELETE' });
}

export async function sendChatMessage(payload: ChatMessageRequest): Promise<ChatMessageResponse> {
  const response = await fetch(`${resolveApiBaseUrl()}/api/chat/message`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json'
    },
    credentials: 'include',
    body: JSON.stringify(payload)
  });

  if (!response.ok) {
    const text = await response.text();
    throw new Error(text || `Request failed with status ${response.status}`);
  }

  return response.json() as Promise<ChatMessageResponse>;
}

export async function streamChatMessage(
  payload: ChatMessageRequest,
  onChunk: (chunk: string) => void,
  signal?: AbortSignal
): Promise<void> {
  const response = await fetch(`${resolveApiBaseUrl()}/api/chat/stream`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Accept: 'text/event-stream'
    },
    credentials: 'include',
    signal,
    body: JSON.stringify(payload)
  });

  if (!response.ok || !response.body) {
    const text = await response.text();
    throw new Error(text || `Stream request failed with status ${response.status}`);
  }

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  const eventSeparatorPattern = /\r?\n\r?\n/;

  function flushEventBlock(block: string) {
    const lines = block.split(/\r?\n/);
    const dataLines: string[] = [];

    for (const line of lines) {
      if (line.startsWith('data:')) {
        dataLines.push(line.startsWith('data: ') ? line.slice(6) : line.slice(5));
      }
    }

    if (dataLines.length === 0) {
      return;
    }

    const chunk = dataLines.join('\n').replace(/^data:\s?/gm, '');
    if (chunk.length > 0) {
      onChunk(chunk);
    }
  }

  while (true) {
    const { done, value } = await reader.read();
    buffer += decoder.decode(value ?? new Uint8Array(), { stream: !done });

    let match = eventSeparatorPattern.exec(buffer);
    while (match) {
      const separator = match[0];
      const separatorIndex = match.index;
      const eventBlock = buffer.slice(0, separatorIndex);
      buffer = buffer.slice(separatorIndex + separator.length);
      flushEventBlock(eventBlock);
      match = eventSeparatorPattern.exec(buffer);
    }

    if (done) {
      break;
    }
  }

  const finalBlock = buffer.trim();
  if (finalBlock.length > 0) {
    flushEventBlock(finalBlock);
  }
}
