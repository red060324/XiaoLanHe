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

function resolveApiBaseUrl(): string {
  const configured = import.meta.env.VITE_API_BASE_URL;
  return configured && configured.trim().length > 0 ? configured : DEFAULT_API_BASE_URL;
}

export async function sendChatMessage(payload: ChatMessageRequest): Promise<ChatMessageResponse> {
  const response = await fetch(`${resolveApiBaseUrl()}/api/chat/message`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json'
    },
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
  onChunk: (chunk: string) => void
): Promise<void> {
  const response = await fetch(`${resolveApiBaseUrl()}/api/chat/stream`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Accept: 'text/event-stream'
    },
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
