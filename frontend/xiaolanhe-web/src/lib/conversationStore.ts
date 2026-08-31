export type ConversationMessage = {
  id: string;
  role: 'user' | 'assistant';
  content: string;
};

export type ConversationRecord = {
  id: string;
  title: string;
  sessionId?: string;
  messages: ConversationMessage[];
  createdAt: string;
  updatedAt: string;
};

const STORAGE_KEY = 'xiaolanhe_local_conversations';

function storageKey(userId?: string): string {
  return `${STORAGE_KEY}:${userId ? `user:${userId}` : 'guest'}`;
}

export function loadConversations(userId?: string): ConversationRecord[] {
  try {
    const raw = localStorage.getItem(storageKey(userId));
    if (!raw) {
      return [];
    }
    const parsed = JSON.parse(raw) as ConversationRecord[];
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

export function saveConversations(records: ConversationRecord[], userId?: string): void {
  localStorage.setItem(storageKey(userId), JSON.stringify(records));
}

export function createConversation(): ConversationRecord {
  const now = new Date().toISOString();
  return {
    id: crypto.randomUUID(),
    title: '新对话',
    messages: [],
    createdAt: now,
    updatedAt: now
  };
}

export function buildConversationTitle(firstUserMessage: string): string {
  const normalized = firstUserMessage.trim().replace(/\s+/g, ' ');
  if (!normalized) {
    return '新对话';
  }
  return normalized.length > 24 ? `${normalized.slice(0, 24)}...` : normalized;
}
