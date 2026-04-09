import { FormEvent, KeyboardEvent, useEffect, useMemo, useRef, useState } from 'react';
import ChatMessageList from './components/ChatMessageList';
import { streamChatMessage } from './lib/api';
import {
  buildConversationTitle,
  ConversationRecord,
  createConversation,
  loadConversations,
  saveConversations
} from './lib/conversationStore';

const isLoggedIn = false;

function sanitizeChunk(chunk: string): string {
  return chunk.replace(/^data:\s?/gm, '');
}

function SidebarToggleIcon({ collapsed }: { collapsed: boolean }) {
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true">
      <rect x="3" y="4" width="18" height="16" rx="3" fill="none" stroke="currentColor" strokeWidth="1.8" />
      {collapsed ? (
        <path d="M10 7h1v10h-1z" fill="currentColor" />
      ) : (
        <path d="M13 7h1v10h-1z" fill="currentColor" />
      )}
    </svg>
  );
}

function NewChatIcon() {
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true">
      <path
        d="M13.5 5.5 18.5 10.5M7 17l2.7-.5L18 8.2a1.8 1.8 0 0 0 0-2.5l-.7-.7a1.8 1.8 0 0 0-2.5 0L6.5 13.3 6 16z"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.8"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

export default function App() {
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const [conversations, setConversations] = useState<ConversationRecord[]>(() => {
    const existing = loadConversations();
    return existing.length > 0 ? existing : [createConversation()];
  });
  const [activeConversationId, setActiveConversationId] = useState<string>(() => {
    const existing = loadConversations();
    return existing[0]?.id ?? createConversation().id;
  });
  const [input, setInput] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const conversationStageRef = useRef<HTMLElement | null>(null);

  const activeConversation = useMemo(() => {
    return conversations.find((item) => item.id === activeConversationId) ?? conversations[0];
  }, [activeConversationId, conversations]);
  const lastMessageContent = activeConversation?.messages[activeConversation.messages.length - 1]?.content ?? '';

  const canSubmit = useMemo(() => input.trim().length > 0 && !loading, [input, loading]);

  useEffect(() => {
    saveConversations(conversations);
  }, [conversations]);

  useEffect(() => {
    if (!activeConversation && conversations.length > 0) {
      setActiveConversationId(conversations[0].id);
    }
  }, [activeConversation, conversations]);

  useEffect(() => {
    const container = conversationStageRef.current;
    if (!container) {
      return;
    }

    const frame = window.requestAnimationFrame(() => {
      container.scrollTo({
        top: container.scrollHeight,
        behavior: 'auto'
      });
    });

    return () => window.cancelAnimationFrame(frame);
  }, [
    activeConversationId,
    activeConversation?.messages.length,
    lastMessageContent,
    loading
  ]);

  function handleNewChat() {
    const next = createConversation();
    setConversations((current) => [next, ...current]);
    setActiveConversationId(next.id);
    setInput('');
    setError(null);
    setLoading(false);
  }

  async function submitMessage() {
    const content = input.trim();
    if (!content || loading || !activeConversation) {
      return;
    }

    const assistantId = crypto.randomUUID();
    const title =
      activeConversation.messages.length === 0 ? buildConversationTitle(content) : activeConversation.title;
    const now = new Date().toISOString();
    const resolvedSessionId = activeConversation.sessionId ?? activeConversation.id;

    setConversations((current) =>
      current.map((conversation) =>
        conversation.id === activeConversation.id
          ? {
              ...conversation,
              title,
              sessionId: resolvedSessionId,
              updatedAt: now,
              messages: [
                ...conversation.messages,
                {
                  id: crypto.randomUUID(),
                  role: 'user',
                  content
                },
                {
                  id: assistantId,
                  role: 'assistant',
                  content: ''
                }
              ]
            }
          : conversation
      )
    );

    setInput('');
    setError(null);
    setLoading(true);

    try {
      await streamChatMessage(
        {
          sessionId: resolvedSessionId,
          message: content
        },
        (chunk) => {
          const normalizedChunk = sanitizeChunk(chunk);
          setConversations((current) =>
            current.map((conversation) =>
              conversation.id === activeConversation.id
                ? {
                    ...conversation,
                    updatedAt: new Date().toISOString(),
                    messages: conversation.messages.map((message) =>
                      message.id === assistantId
                        ? { ...message, content: message.content + normalizedChunk }
                        : message
                    )
                  }
                : conversation
            )
          );
        }
      );
    } catch (requestError) {
      const message = requestError instanceof Error ? requestError.message : '请求失败';
      setError(message);
    } finally {
      setLoading(false);
    }
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    await submitMessage();
  }

  async function handleComposerKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      await submitMessage();
    }
  }

  return (
    <div className={`gpt-shell ${sidebarCollapsed ? 'sidebar-collapsed' : ''}`}>
      <aside className="sidebar">
        <div className="sidebar-top">
          <div className="brand-row">
            {!sidebarCollapsed ? <div className="brand-title">小蓝盒</div> : <div className="brand-placeholder" />}
            <button
              className="collapse-button"
              type="button"
              aria-label={sidebarCollapsed ? '展开侧边栏' : '收起侧边栏'}
              onClick={() => setSidebarCollapsed((current) => !current)}
            >
              <SidebarToggleIcon collapsed={sidebarCollapsed} />
            </button>
          </div>

          <button className="nav-item primary" type="button" onClick={handleNewChat}>
            <span className="nav-icon">
              <NewChatIcon />
            </span>
            {!sidebarCollapsed ? <span>新聊天</span> : null}
          </button>

          {!sidebarCollapsed ? (
            <section className="history-panel">
              <div className="history-title">最近</div>
              <div className="history-list">
                {conversations.map((conversation) => (
                  <button
                    key={conversation.id}
                    type="button"
                    className={`history-item ${conversation.id === activeConversation?.id ? 'active' : ''}`}
                    onClick={() => setActiveConversationId(conversation.id)}
                  >
                    <span className="history-item-title">{conversation.title}</span>
                  </button>
                ))}
              </div>
            </section>
          ) : null}
        </div>

        {!sidebarCollapsed ? (
          <div className="sidebar-footer">
            <div className="login-card">
              <p className="login-title">当前会话记录</p>
              <p className="login-copy">
                {isLoggedIn
                  ? '登录后将展示当前用户的历史会话记录。'
                  : '当前未登录，左侧展示的是本地临时会话记录。'}
              </p>
              <button className="login-primary" type="button">
                登录
              </button>
            </div>
          </div>
        ) : null}
      </aside>

      <main className="main-stage">
        <header className="topbar">
          <div className="topbar-title">小蓝盒</div>
          <div className="topbar-actions">
            <button className="ghost-button" type="button">
              登录
            </button>
            <button className="outline-button" type="button">
              免费注册
            </button>
          </div>
        </header>

        <section className="conversation-stage" ref={conversationStageRef}>
          {activeConversation?.messages.length ? (
            <ChatMessageList messages={activeConversation.messages} loading={loading} />
          ) : (
            <div className="welcome-block">
              <h1>我们先从哪里开始呢？</h1>
            </div>
          )}
        </section>

        <section className="composer-shell">
          <form className="composer-card" onSubmit={handleSubmit}>
            <button className="composer-add" type="button" aria-label="新建能力入口">
              +
            </button>
            <textarea
              value={input}
              onChange={(event) => setInput(event.target.value)}
              onKeyDown={handleComposerKeyDown}
              placeholder="有问题，尽管问"
              rows={1}
            />
            <button className="composer-send" type="submit" disabled={!canSubmit}>
              {loading ? '生成中' : '发送'}
            </button>
          </form>

          {error ? <div className="error-banner">请求失败：{error}</div> : null}
        </section>
      </main>
    </div>
  );
}
