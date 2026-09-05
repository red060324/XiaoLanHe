import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import type { ConversationMessage } from '../lib/conversationStore';
import ChatMessageList from './ChatMessageList';

const liveStatusSelector = '[role="status"], [aria-live="polite"]';

function getMessageRow(content: string): HTMLElement {
  const row = screen.getByText(content).closest('article');
  if (!(row instanceof HTMLElement)) {
    throw new Error(`No message row found for: ${content}`);
  }
  return row;
}

function findSelfOrDescendant(row: HTMLElement, selector: string): HTMLElement | null {
  return row.matches(selector)
    ? row
    : row.querySelector<HTMLElement>(selector);
}

afterEach(cleanup);

describe('ChatMessageList', () => {
  it('exposes only the streaming assistant response as a polite busy status', () => {
    const messages: ConversationMessage[] = [
      { id: 'assistant-history', role: 'assistant', content: 'Earlier assistant answer' },
      { id: 'user-follow-up', role: 'user', content: 'Follow-up question' },
      { id: 'assistant-active', role: 'assistant', content: 'Current assistant answer' }
    ];
    const view = render(<ChatMessageList messages={messages} loading />);

    const historicalRow = getMessageRow('Earlier assistant answer');
    const streamingRow = getMessageRow('Current assistant answer');
    const streamingStatus = findSelfOrDescendant(streamingRow, liveStatusSelector);

    expect(streamingStatus).not.toBeNull();
    expect(streamingStatus).toHaveAttribute('aria-busy', 'true');
    expect(findSelfOrDescendant(historicalRow, liveStatusSelector)).toBeNull();
    expect(screen.queryAllByRole('alert')).toHaveLength(0);

    view.rerender(<ChatMessageList messages={messages} loading={false} />);

    const completedRow = getMessageRow('Current assistant answer');
    const completedStatus = findSelfOrDescendant(completedRow, liveStatusSelector);

    expect(findSelfOrDescendant(completedRow, '[aria-busy="true"]')).toBeNull();
    if (completedStatus) {
      expect(completedStatus).toHaveAttribute('aria-busy', 'false');
    }
  });
});
