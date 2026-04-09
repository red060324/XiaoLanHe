import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { ConversationMessage } from '../lib/conversationStore';

type Props = {
  messages: ConversationMessage[];
  loading: boolean;
};

function normalizeAssistantContent(content: string): string {
  return content
    .replace(/^data:\s?/gm, '')
    .replace(/\r\n/g, '\n')
    .replace(/\u00a0/g, ' ')
    .replace(/[ \t]*\n[ \t]*(#{1,6}[ \t]*\d+\.)/g, '\n\n$1')
    .replace(/[ \t]*\n[ \t]*(-[ \t]+\*\*[^*\n]+\*\*[:：]?)/g, '\n$1')
    .replace(/^[ \t]+(?=#{1,6}\s)/gm, '')
    .replace(/^[ \t]+(?=-\s)/gm, '')
    .replace(/^(#{1,6})\s*(\d+\.)\s*/gm, '$1 $2 ')
    .replace(/^(#{1,6})(\S)/gm, '$1 $2')
    .replace(/^(\s*)-\s*([^\s])/gm, '$1- $2')
    .replace(/^(\s*)\*\s+\*\*([^*]+)\*\*[:：]?\s*/gm, '$1- **$2**：')
    .replace(/^(\s*)\*\s+\*([^*]+)\*[:：]?\s*/gm, '$1- **$2**：')
    .replace(/^(\s*)\*{1,2}([^*\n]+)\*{1,2}[:：]?\s*/gm, '$1- **$2**：')
    .trim();
}

export default function ChatMessageList({ messages, loading }: Props) {
  return (
    <div className="message-list">
      {messages.map((message) => (
        <article key={message.id} className={`message-row ${message.role}`}>
          {message.role === 'assistant' ? <div className="avatar">盒</div> : null}
          <div className="message-bubble">
            {message.role === 'assistant' ? (
              <div className="message-markdown">
                <ReactMarkdown remarkPlugins={[remarkGfm]}>
                  {normalizeAssistantContent(message.content)}
                </ReactMarkdown>
              </div>
            ) : (
              <div className="message-text">{message.content}</div>
            )}
          </div>
        </article>
      ))}
      {loading && messages.length === 0 ? (
        <article className="message-row assistant">
          <div className="avatar">盒</div>
          <div className="message-bubble">
            <div className="message-text">正在生成中...</div>
          </div>
        </article>
      ) : null}
    </div>
  );
}
