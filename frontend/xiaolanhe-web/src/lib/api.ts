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
  editions?: Array<{ id: string; code: string; name: string; description?: string; price?: Price }>;
};

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
