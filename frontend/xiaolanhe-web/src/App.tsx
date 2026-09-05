import { FormEvent, KeyboardEvent, lazy, Suspense, useEffect, useMemo, useRef, useState } from 'react';
import AccountSettings from './components/AccountSettings';
import CommercePage, { createCommerceIdempotencyKeys } from './components/CommercePage';
import CommunityPage from './components/CommunityPage';
import {
  Game,
  User,
  getGame,
  getMe,
  listGames,
  login,
  logout,
  register,
  streamChatMessage
} from './lib/api';
import {
  buildConversationTitle,
  ConversationRecord,
  createConversation,
  loadConversations,
  saveConversations
} from './lib/conversationStore';

const ChatMessageList = lazy(() => import('./components/ChatMessageList'));

type View = 'assistant' | 'discover' | 'community' | 'commerce' | 'orders' | 'account';
type AuthMode = 'login' | 'register';
type ActiveStream = {
  controller: AbortController;
  conversationId: string;
  assistantMessageId: string;
};

function formatPrice(game: Game): string {
	const price = game.editions?.find((edition) => edition.price)?.price;
	return price ? `${price.currency} ${(price.amountMinor / 100).toFixed(2)}` : '暂未定价';
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
	const [view, setView] = useState<View>('assistant');
	const [user, setUser] = useState<User | null>(null);
	const [games, setGames] = useState<Game[]>([]);
	const [purchaseGames, setPurchaseGames] = useState<Game[]>([]);
	const [selectedGame, setSelectedGame] = useState<Game | null>(null);
	const [catalogQuery, setCatalogQuery] = useState('');
	const [catalogLoading, setCatalogLoading] = useState(false);
	const [authMode, setAuthMode] = useState<AuthMode>('login');
	const [username, setUsername] = useState('');
	const [displayName, setDisplayName] = useState('');
	const [password, setPassword] = useState('');
	const [authBusy, setAuthBusy] = useState(false);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const [conversations, setConversations] = useState<ConversationRecord[]>(() => {
    const existing = loadConversations();
    return existing.length > 0 ? existing : [createConversation()];
  });
  const [activeConversationId, setActiveConversationId] = useState<string>(() => conversations[0].id);
  const [input, setInput] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const conversationStageRef = useRef<HTMLElement | null>(null);
	const activeStreamRef = useRef<ActiveStream | null>(null);
	const authStateVersionRef = useRef(0);
	const authRequestPendingRef = useRef<number | null>(null);
	const catalogRequestRef = useRef(0);
  const commerceKeysRef = useRef(createCommerceIdempotencyKeys());
  const commerceKeysOwnerRef = useRef<User['id'] | null>(null);

  function navigate(nextView: View) {
    if (view === 'discover' && nextView !== 'discover') {
      catalogRequestRef.current += 1;
      setCatalogLoading(false);
    }
    setError(null);
    setView(nextView);
  }

  const activeConversation = useMemo(() => {
    return conversations.find((item) => item.id === activeConversationId) ?? conversations[0];
  }, [activeConversationId, conversations]);
  const lastMessageContent = activeConversation?.messages[activeConversation.messages.length - 1]?.content ?? '';

  const canSubmit = useMemo(() => input.trim().length > 0 && !loading, [input, loading]);

  useEffect(() => {
    saveConversations(conversations, user?.id);
  }, [conversations, user?.id]);

  useEffect(() => {
    const userId = user?.id ?? null;
    if (commerceKeysOwnerRef.current === userId) return;
    commerceKeysOwnerRef.current = userId;
    commerceKeysRef.current.claims.clear();
    commerceKeysRef.current.orders.clear();
    commerceKeysRef.current.payments.clear();
    commerceKeysRef.current.flashSales.clear();
  }, [user?.id]);

	useEffect(() => {
		const authStateVersion = authStateVersionRef.current;
		void getMe().then((nextUser) => {
			if (authStateVersion !== authStateVersionRef.current) return;
			setUser(nextUser);
			if (nextUser) switchConversationOwner(nextUser.id);
		}).catch((requestError) => {
			if (authStateVersion === authStateVersionRef.current) {
				setError(requestError instanceof Error ? requestError.message : '账号状态加载失败');
			}
		});
		void loadCatalog('');
		return () => activeStreamRef.current?.controller.abort();
	}, []);

  useEffect(() => {
    if (!activeConversation && conversations.length > 0) {
      setActiveConversationId(conversations[0].id);
    }
  }, [activeConversation, conversations]);

	useEffect(() => {
		if (view !== 'commerce' && view !== 'orders') return;
		let active = true;
		setPurchaseGames([]);
		setError(null);
		// ponytail: bounded by the 50-item catalog page; add a batch offers endpoint when this becomes measurable.
		void Promise.all(games.map((game) => game.editions ? game : getGame(game.slug))).then((details) => {
			if (active) setPurchaseGames(details);
		}).catch((requestError) => {
			if (active) setError(requestError instanceof Error ? requestError.message : '购买信息加载失败');
		});
		return () => { active = false; };
	}, [games, view]);

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
		cancelActiveStream();
		navigate('assistant');
    const next = createConversation();
    setConversations((current) => [next, ...current]);
    setActiveConversationId(next.id);
    setInput('');
    setError(null);
    setLoading(false);
  }

  function switchConversationOwner(userId?: string) {
		cancelActiveStream();
    const existing = loadConversations(userId);
    const next = existing.length > 0 ? existing : [createConversation()];
    setConversations(next);
    setActiveConversationId(next[0].id);
    setInput('');
    setError(null);
    setLoading(false);
  }

  function cancelActiveStream() {
    const stream = activeStreamRef.current;
    activeStreamRef.current = null;
    stream?.controller.abort();
    if (stream) {
      setConversations((current) =>
        current.map((conversation) =>
          conversation.id === stream.conversationId
            ? {
                ...conversation,
                messages: conversation.messages.map((message) =>
                  message.id === stream.assistantMessageId && message.content === ''
                    ? { ...message, content: '已停止生成。' }
                    : message
                )
              }
            : conversation
        )
      );
    }
    setLoading(false);
  }

  function selectConversation(conversationId: string) {
    cancelActiveStream();
    if (conversationId === activeConversation?.id) {
      return;
    }
    setActiveConversationId(conversationId);
    setInput('');
    setError(null);
  }

  async function submitMessage() {
    const content = input.trim();
    if (!content || loading || !activeConversation) {
      return;
    }

    const localConversationId = activeConversation.id;
    const assistantId = crypto.randomUUID();
    const title =
      activeConversation.messages.length === 0 ? buildConversationTitle(content) : activeConversation.title;
    const now = new Date().toISOString();
    const resolvedSessionId = activeConversation.sessionId ?? localConversationId;

    setConversations((current) =>
      current.map((conversation) =>
        conversation.id === localConversationId
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
		const controller = new AbortController();
		activeStreamRef.current = { controller, conversationId: localConversationId, assistantMessageId: assistantId };

    try {
      const result = await streamChatMessage(
        {
          sessionId: resolvedSessionId,
          message: content
        },
        (chunk) => {
          if (controller.signal.aborted || activeStreamRef.current?.controller !== controller) {
            return;
          }
          setConversations((current) =>
            current.map((conversation) =>
              conversation.id === localConversationId
                ? {
                    ...conversation,
                    updatedAt: new Date().toISOString(),
                    messages: conversation.messages.map((message) =>
                      message.id === assistantId
                        ? { ...message, content: message.content + chunk }
                        : message
                    )
                  }
                : conversation
            )
          );
			},
			controller.signal
      );
      const canonicalConversationId = result?.conversationId?.trim();
      if (canonicalConversationId && !controller.signal.aborted && activeStreamRef.current?.controller === controller) {
        setConversations((current) =>
          current.map((conversation) =>
            conversation.id === localConversationId
              ? { ...conversation, sessionId: canonicalConversationId }
              : conversation
          )
        );
      }
    } catch (requestError) {
		if (controller.signal.aborted || (requestError instanceof DOMException && requestError.name === 'AbortError')) {
			return;
		}
      if (activeStreamRef.current?.controller !== controller) {
        return;
      }
      const message = requestError instanceof Error ? requestError.message : '请求失败';
      setError(message);
      setConversations((current) =>
        current.map((conversation) =>
          conversation.id === localConversationId
            ? {
                ...conversation,
                messages: conversation.messages.map((message) =>
                  message.id === assistantId && message.content === ''
                    ? { ...message, content: '生成失败，请重试。' }
                    : message
                )
              }
            : conversation
        )
      );
    } finally {
      if (activeStreamRef.current?.controller === controller) {
        activeStreamRef.current = null;
        setLoading(false);
      }
    }
  }

	async function loadCatalog(query: string) {
		const request = ++catalogRequestRef.current;
		setCatalogLoading(true);
		setError(null);
		try {
			const nextGames = await listGames(query);
			if (request !== catalogRequestRef.current) return;
			setGames(nextGames);
		} catch (requestError) {
			if (request === catalogRequestRef.current) {
				setError(requestError instanceof Error ? requestError.message : '游戏目录加载失败');
			}
		} finally {
			if (request === catalogRequestRef.current) setCatalogLoading(false);
		}
	}

	async function openGame(slug: string) {
		const request = ++catalogRequestRef.current;
		setCatalogLoading(true);
		try {
			const game = await getGame(slug);
			if (request !== catalogRequestRef.current) return;
			setSelectedGame(game);
		} catch (requestError) {
			if (request === catalogRequestRef.current) {
				setError(requestError instanceof Error ? requestError.message : '游戏详情加载失败');
			}
		} finally {
			if (request === catalogRequestRef.current) setCatalogLoading(false);
		}
	}

	async function submitAuth(event: FormEvent<HTMLFormElement>) {
		event.preventDefault();
		if (authRequestPendingRef.current !== null) return;
		const authStateVersion = ++authStateVersionRef.current;
		authRequestPendingRef.current = authStateVersion;
		setAuthBusy(true);
		setError(null);
		try {
			const nextUser = authMode === 'login' ? await login(username, password) : await register(username, displayName, password);
			if (authStateVersion !== authStateVersionRef.current) return;
			setUser(nextUser);
			switchConversationOwner(nextUser.id);
			setPassword('');
			navigate('assistant');
		} catch (requestError) {
			if (authStateVersion === authStateVersionRef.current) {
				setError(requestError instanceof Error ? requestError.message : '认证失败');
			}
		} finally {
			if (authRequestPendingRef.current === authStateVersion) {
				authRequestPendingRef.current = null;
				setAuthBusy(false);
			}
		}
	}

	async function signOut() {
		if (authRequestPendingRef.current !== null) return;
		const authStateVersion = ++authStateVersionRef.current;
		authRequestPendingRef.current = authStateVersion;
		setAuthBusy(true);
		setError(null);
		try {
			await logout();
			if (authStateVersion !== authStateVersionRef.current) return;
			setUser(null);
			switchConversationOwner();
			navigate('assistant');
		} catch (requestError) {
			if (authStateVersion === authStateVersionRef.current) {
				setError(requestError instanceof Error ? requestError.message : '退出登录失败');
			}
		} finally {
			if (authRequestPendingRef.current === authStateVersion) {
				authRequestPendingRef.current = null;
				setAuthBusy(false);
			}
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
            <button className="collapse-button" type="button" aria-label={sidebarCollapsed ? '展开侧边栏' : '收起侧边栏'} onClick={() => setSidebarCollapsed((current) => !current)}>
              <SidebarToggleIcon collapsed={sidebarCollapsed} />
            </button>
          </div>
          <nav className="primary-navigation" aria-label="主要导航">
            <button className={`nav-item ${view === 'discover' ? 'active' : ''}`} type="button" aria-label="发现游戏" onClick={() => navigate('discover')}>
              <span className="nav-icon">⌕</span><span className="nav-label">发现游戏</span>
            </button>
            <button className={`nav-item ${view === 'community' ? 'active' : ''}`} type="button" aria-label="游戏社区" onClick={() => navigate('community')}>
              <span className="nav-icon">◎</span><span className="nav-label">游戏社区</span>
            </button>
            <button className={`nav-item ${view === 'commerce' ? 'active' : ''}`} type="button" aria-label="优惠商店" onClick={() => navigate('commerce')}>
              <span className="nav-icon">%</span><span className="nav-label">优惠商店</span>
            </button>
            <button className={`nav-item primary ${view === 'assistant' ? 'active' : ''}`} type="button" aria-label="游戏助手" onClick={handleNewChat}>
              <span className="nav-icon"><NewChatIcon /></span><span className="nav-label">游戏助手</span>
            </button>
            <button className={`nav-item ${view === 'orders' ? 'active' : ''}`} type="button" aria-label="我的订单" onClick={() => navigate('orders')}>
              <span className="nav-icon">▤</span><span className="nav-label">我的订单</span>
            </button>
            <button className={`nav-item ${view === 'account' ? 'active' : ''}`} type="button" aria-label="账号" onClick={() => navigate('account')}>
              <span className="nav-icon">○</span><span className="nav-label">账号</span>
            </button>
          </nav>
          {!sidebarCollapsed && view === 'assistant' ? (
            <section className="history-panel">
              <div className="history-title">最近</div>
              <div className="history-list">
                {conversations.map((conversation) => (
                  <button key={conversation.id} type="button" className={`history-item ${conversation.id === activeConversation?.id ? 'active' : ''}`} onClick={() => selectConversation(conversation.id)}>
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
              <p className="login-title">{user ? user.displayName : '游客模式'}</p>
              <p className="login-copy">{user ? `@${user.username} · ${user.role}` : '登录后可以领取优惠、购买游戏并查看已拥有内容。'}</p>
              <button className="login-primary" type="button" disabled={authBusy} onClick={() => user ? void signOut() : navigate('account')}>{user ? '退出登录' : '登录 / 注册'}</button>
            </div>
          </div>
        ) : null}
      </aside>

      <main className="main-stage">
        <header className="topbar">
          <div className="topbar-title">{view === 'discover' ? '发现游戏' : view === 'community' ? '游戏社区' : view === 'commerce' ? '优惠商店' : view === 'orders' ? '我的订单' : view === 'account' ? '账号' : '游戏助手'}</div>
          <div className="topbar-actions">
            <button className="outline-button" type="button" onClick={() => navigate('discover')}>游戏库</button>
            <button className="ghost-button" type="button" aria-label={user?.displayName ?? '打开账号'} onClick={() => navigate('account')}>{user?.displayName ?? '登录'}</button>
          </div>
        </header>

        {view === 'assistant' ? (
          <>
            <section className="conversation-stage" ref={conversationStageRef}>
              {activeConversation?.messages.length ? (
                <Suspense fallback={<div className="message-render-loading" role="status">正在加载消息渲染器...</div>}>
                  <ChatMessageList messages={activeConversation.messages} loading={loading} />
                </Suspense>
              ) : <div className="welcome-block"><h1>想查哪款游戏？</h1><p>攻略、版本信息和社区内容都可以问我。</p></div>}
            </section>
            <section className="composer-shell">
              <form className="composer-card" onSubmit={handleSubmit}>
                <button className="composer-add" type="button" aria-label="打开游戏目录" onClick={() => navigate('discover')}>+</button>
                <textarea value={input} onChange={(event) => setInput(event.target.value)} onKeyDown={handleComposerKeyDown} placeholder="问攻略、版本或社区内容" rows={1} />
                {loading ? <button className="composer-send" type="button" onClick={cancelActiveStream}>停止</button> : <button className="composer-send" type="submit" disabled={!canSubmit}>发送</button>}
              </form>
              {error ? <div className="error-banner" role="alert">请求失败：{error}</div> : null}
            </section>
          </>
        ) : null}

        {view === 'discover' ? (
          <section className="page-stage">
            <form className="catalog-search" onSubmit={(event) => { event.preventDefault(); void loadCatalog(catalogQuery); }}>
              <label htmlFor="catalog-query">搜索游戏</label>
              <div><input id="catalog-query" value={catalogQuery} onChange={(event) => setCatalogQuery(event.target.value)} placeholder="游戏名称或标识" /><button type="submit">搜索</button></div>
            </form>
            {error ? <div className="error-banner" role="alert">{error}</div> : null}
            {selectedGame ? (
              <article className="game-detail">
                <button type="button" onClick={() => setSelectedGame(null)}>← 返回目录</button>
                <h1>{selectedGame.name}</h1><p>{selectedGame.description || selectedGame.summary}</p>
                <p>{selectedGame.developer || '未知开发商'} · {selectedGame.releaseDate || '待定'}</p>
                <div className="edition-list">{selectedGame.editions?.map((edition) => <div key={edition.id}><strong>{edition.name}</strong><span>{edition.price ? `${edition.price.currency} ${(edition.price.amountMinor / 100).toFixed(2)}` : '暂未定价'}</span></div>)}</div>
              </article>
            ) : catalogLoading ? <p className="empty-state">正在加载游戏目录…</p> : games.length === 0 ? <p className="empty-state">没有找到游戏。</p> : (
              <div className="game-grid">{games.map((game) => <button className="game-card" type="button" key={game.id} onClick={() => void openGame(game.slug)}><span className="game-cover">{game.name.slice(0, 1)}</span><strong>{game.name}</strong><span>{game.summary || '查看游戏详情'}</span><small>{game.owned ? '已拥有' : formatPrice(game)}</small></button>)}</div>
            )}
          </section>
        ) : null}

        {view === 'community' ? <CommunityPage user={user} games={games} onRequireLogin={() => navigate('account')} /> : null}

		{view === 'commerce' || view === 'orders' ? <section className="page-stage commerce-route-stage">{error ? <div className="error-banner" role="alert">{error}</div> : null}<CommercePage initialTab={view === 'orders' ? 'orders' : 'deals'} user={user} games={purchaseGames} idempotencyKeys={commerceKeysRef.current} onRequireLogin={() => navigate('account')} onOwned={() => void loadCatalog(catalogQuery)} onTabChange={(tab) => setView(tab === 'orders' ? 'orders' : 'commerce')} /></section> : null}

        {view === 'account' ? (
          <section className="page-stage auth-stage">
            {user ? <AccountSettings user={user} authBusy={authBusy} onSignOut={() => void signOut()} /> : (
              <form className="auth-card" aria-busy={authBusy} onSubmit={submitAuth}>
                <h1>{authMode === 'login' ? '登录小蓝盒' : '注册小蓝盒'}</h1>
                <label>用户名<input autoComplete="username" value={username} onChange={(event) => setUsername(event.target.value)} required /></label>
                {authMode === 'register' ? <label>显示名称<input value={displayName} onChange={(event) => setDisplayName(event.target.value)} required /></label> : null}
                <label>密码<input type="password" autoComplete={authMode === 'login' ? 'current-password' : 'new-password'} minLength={8} maxLength={72} value={password} onChange={(event) => setPassword(event.target.value)} required /></label>
                <button type="submit" disabled={authBusy}>{authMode === 'login' ? '登录' : '创建账号'}</button>
                <button className="text-button" type="button" disabled={authBusy} onClick={() => setAuthMode(authMode === 'login' ? 'register' : 'login')}>{authMode === 'login' ? '没有账号？立即注册' : '已有账号？返回登录'}</button>
                {error ? <div className="error-banner" role="alert">{error}</div> : null}
              </form>
            )}
          </section>
        ) : null}
      </main>
    </div>
  );
}
