import { Streamdown } from 'streamdown';
import { ConversationMessage } from '../lib/conversationStore';

type Props = {
  messages: ConversationMessage[];
  loading: boolean;
};

function normalizeAssistantContent(content: string): string {
  return content
    .replace(/\r\n/g, '\n')
    .replace(/\u00a0/g, ' ')
    .replace(/(^|\n)\s*(#{1,6})\s*\n+\s*(\d+\.\s*[^\n]+)/g, '$1$2 $3')
    .replace(/(^|\n)\s*(#{1,6})\s*\n+\s*([^\n#-][^\n]*)/g, '$1$2 $3')
    .replace(/(^|\n)\s*(#{1,6})\s*([^\s#][^\n]*)/g, '$1$2 $3')
    .replace(/(^|\n)\s*(\d+)\.\s*([^\n]{1,40})(?=\n(?:- |\* |\d+\.|$))/g, '$1### $2. $3')
    .replace(/(^|\n)\s*(\d+)\.\s*([^\n]+)/g, '$1$2. $3')
    .replace(/([^\n])\n(#{1,6}\s)/g, '$1\n\n$2')
    .replace(/([。！？：:])\s*(#{1,6}\s)/g, '$1\n\n$2')
    .replace(/([。！？])\s*(-\s+\*\*|- |\d+\.\s)/g, '$1\n$2')
    .replace(/([^\n])\n(-\s+\*\*)/g, '$1\n\n$2')
    .replace(/([^\n])\n(-\s)/g, '$1\n$2')
    .replace(/([：:])\n(?=-\s)/g, '$1\n\n')
    .replace(/([：:])\n(?=\d+\.\s)/g, '$1\n\n')
    .replace(/([：:])\s+(-\s)/g, '$1\n\n$2')
    .replace(/([：:])\s+(\d+\.\s)/g, '$1\n\n$2')
    .replace(/\n{3,}/g, '\n\n')
    .trim();
}

export default function ChatMessageList({ messages, loading }: Props) {
  const lastAssistantMessageId = [...messages].reverse().find((message) => message.role === 'assistant')?.id;

  return (
    <div className="message-list">
      {messages.map((message) => {
        const isStreamingAssistant = loading && message.role === 'assistant' && message.id === lastAssistantMessageId;
        return (
          <article
            key={message.id}
            className={`message-row ${message.role}`}
            role={isStreamingAssistant ? 'status' : undefined}
            aria-live={isStreamingAssistant ? 'polite' : undefined}
            aria-busy={isStreamingAssistant ? true : undefined}
          >
            {message.role === 'assistant' ? <div className="avatar">盒</div> : null}
            <div className="message-bubble">
              {message.role === 'assistant' ? (
                <div className="message-markdown">
                  <Streamdown
                    mode={loading && message.id === lastAssistantMessageId ? 'streaming' : 'static'}
                    parseIncompleteMarkdown={loading && message.id === lastAssistantMessageId}
                    isAnimating={loading && message.id === lastAssistantMessageId}
                  >
                    {normalizeAssistantContent(message.content)}
                  </Streamdown>
                </div>
              ) : (
                <div className="message-text">{message.content}</div>
              )}
            </div>
          </article>
        );
      })}
      {loading && messages.length === 0 ? (
        <article className="message-row assistant" role="status" aria-live="polite" aria-busy="true">
          <div className="avatar">盒</div>
          <div className="message-bubble">
            <div className="message-text">正在生成中...</div>
          </div>
        </article>
      ) : null}
    </div>
  );
}
