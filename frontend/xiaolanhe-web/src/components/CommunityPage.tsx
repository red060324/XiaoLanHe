import { FormEvent, useEffect, useRef, useState } from 'react';
import {
  CommunityComment,
  CommunityPost,
  Game,
  User,
  createCommunityComment,
  createCommunityPost,
  deleteCommunityComment,
  deleteCommunityPost,
  getCommunityPost,
  listCommunityComments,
  listCommunityPosts,
  setCommunityReaction,
  updateCommunityComment,
  updateCommunityPost
} from '../lib/api';

type Props = {
  user: User | null;
  games: Game[];
  onRequireLogin: () => void;
};

const reactions = [
  { type: 'like' as const, label: '赞' },
  { type: 'helpful' as const, label: '有帮助' },
  { type: 'funny' as const, label: '有趣' }
];

export default function CommunityPage({ user, games, onRequireLogin }: Props) {
  const [posts, setPosts] = useState<CommunityPost[]>([]);
  const [nextCursor, setNextCursor] = useState('');
  const [gameId, setGameId] = useState('');
  const [selectedPost, setSelectedPost] = useState<CommunityPost | null>(null);
  const [comments, setComments] = useState<CommunityComment[]>([]);
  const [commentCursor, setCommentCursor] = useState('');
  const [title, setTitle] = useState('');
  const [content, setContent] = useState('');
  const [postGameId, setPostGameId] = useState('');
  const [comment, setComment] = useState('');
  const [editingPost, setEditingPost] = useState(false);
  const [editingComment, setEditingComment] = useState<string | null>(null);
  const [editingCommentContent, setEditingCommentContent] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const postRequest = useRef(0);
  const detailRequest = useRef(0);
  const selectedPostId = useRef<string | null>(null);
  const commentSubmitting = useRef('');

  useEffect(() => {
    void loadPosts('', false);
  }, []);

  async function loadPosts(cursor: string, append: boolean) {
    const request = ++postRequest.current;
    setLoading(true);
    setError(null);
    try {
      const page = await listCommunityPosts(gameId, cursor);
      if (request !== postRequest.current) return;
      setPosts((current) => append ? [...current, ...page.items] : page.items);
      setNextCursor(page.nextCursor ?? '');
    } catch (requestError) {
      if (request === postRequest.current) setError(messageOf(requestError, '社区加载失败'));
    } finally {
      if (request === postRequest.current) setLoading(false);
    }
  }

  async function openPost(id: string) {
    selectedPostId.current = id;
    const request = ++detailRequest.current;
    setLoading(true);
    setError(null);
    try {
      const [post, page] = await Promise.all([getCommunityPost(id), listCommunityComments(id)]);
      if (request !== detailRequest.current) return;
      setSelectedPost(post);
      setComments(page.items);
      setCommentCursor(page.nextCursor ?? '');
      setEditingPost(false);
    } catch (requestError) {
      if (request === detailRequest.current) setError(messageOf(requestError, '帖子加载失败'));
    } finally {
      if (request === detailRequest.current) setLoading(false);
    }
  }

  async function submitPost(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!user) {
      onRequireLogin();
      return;
    }
    setLoading(true);
    setError(null);
    try {
      const created = await createCommunityPost({ gameId: postGameId || undefined, title, content });
      setPosts((current) => [created, ...current]);
      setTitle('');
      setContent('');
      setPostGameId('');
      detailRequest.current++;
      selectedPostId.current = created.id;
      setSelectedPost(created);
      setComments([]);
      setCommentCursor('');
    } catch (requestError) {
      setError(messageOf(requestError, '发布失败'));
    } finally {
      setLoading(false);
    }
  }

  async function savePost(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selectedPost) return;
    const postId = selectedPost.id;
    setLoading(true);
    setError(null);
    try {
      const updated = await updateCommunityPost(postId, {
        gameId: postGameId || undefined,
        title,
        content
      });
      setPosts((current) => current.map((item) => item.id === updated.id ? updated : item));
      if (selectedPostId.current !== postId) return;
      setSelectedPost(updated);
      setEditingPost(false);
    } catch (requestError) {
      if (selectedPostId.current === postId) setError(messageOf(requestError, '保存失败'));
    } finally {
      if (selectedPostId.current === postId) setLoading(false);
    }
  }

  function beginPostEdit() {
    if (!selectedPost) return;
    setTitle(selectedPost.title);
    setContent(selectedPost.content);
    setPostGameId(selectedPost.game?.id ?? '');
    setEditingPost(true);
  }

  function closePost() {
    detailRequest.current++;
    selectedPostId.current = null;
    setSelectedPost(null);
    setEditingPost(false);
    setTitle('');
    setContent('');
    setPostGameId('');
    setLoading(false);
  }

  async function removePost() {
    if (!selectedPost || !window.confirm('确定删除这篇帖子吗？')) return;
    const postId = selectedPost.id;
    try {
      await deleteCommunityPost(postId);
      setPosts((current) => current.filter((item) => item.id !== postId));
      if (selectedPostId.current !== postId) return;
      closePost();
      setComments([]);
    } catch (requestError) {
      if (selectedPostId.current === postId) setError(messageOf(requestError, '删除失败'));
    }
  }

  async function submitComment(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selectedPost) return;
    if (!user) {
      onRequireLogin();
      return;
    }
    const postId = selectedPost.id;
    if (commentSubmitting.current === postId) return;
    commentSubmitting.current = postId;
    setLoading(true);
    setError(null);
    try {
      const created = await createCommunityComment(postId, comment);
      if (selectedPostId.current !== postId) return;
      setComments((current) => current.some((item) => item.id === created.id) ? current : [...current, created]);
      setSelectedPost((current) => current?.id === postId ? { ...current, commentCount: current.commentCount + 1 } : current);
      setComment('');
    } catch (requestError) {
      if (selectedPostId.current === postId) setError(messageOf(requestError, '评论失败'));
    } finally {
      if (commentSubmitting.current === postId) commentSubmitting.current = '';
      if (selectedPostId.current === postId) setLoading(false);
    }
  }

  async function saveComment(id: string) {
    const postId = selectedPostId.current;
    if (!postId) return;
    try {
      const updated = await updateCommunityComment(id, editingCommentContent);
      if (selectedPostId.current !== postId) return;
      setComments((current) => current.map((item) => item.id === id ? updated : item));
      setEditingComment((current) => current === id ? null : current);
    } catch (requestError) {
      if (selectedPostId.current === postId) setError(messageOf(requestError, '评论保存失败'));
    }
  }

  async function removeComment(id: string) {
    if (!window.confirm('确定删除这条评论吗？')) return;
    const postId = selectedPostId.current;
    if (!postId) return;
    try {
      await deleteCommunityComment(id);
      setPosts((current) => current.map((item) => item.id === postId ? { ...item, commentCount: Math.max(0, item.commentCount - 1) } : item));
      if (selectedPostId.current !== postId) return;
      setComments((current) => current.filter((item) => item.id !== id));
      setSelectedPost((current) => current ? { ...current, commentCount: Math.max(0, current.commentCount - 1) } : current);
    } catch (requestError) {
      if (selectedPostId.current === postId) setError(messageOf(requestError, '评论删除失败'));
    }
  }

  async function toggleReaction(type: 'like' | 'helpful' | 'funny') {
    if (!selectedPost) return;
    if (!user) {
      onRequireLogin();
      return;
    }
    const postId = selectedPost.id;
    const active = !selectedPost.viewerReactions.includes(type);
    try {
      const summary = await setCommunityReaction(postId, type, active);
      setSelectedPost((current) => current?.id === postId ? { ...current, ...summary } : current);
      setPosts((current) => current.map((item) => item.id === postId ? { ...item, ...summary } : item));
    } catch (requestError) {
      setError(messageOf(requestError, '反应更新失败'));
    }
  }

  async function loadMoreComments() {
    if (!selectedPost || !commentCursor) return;
    const request = ++detailRequest.current;
    try {
      const page = await listCommunityComments(selectedPost.id, commentCursor);
      if (request !== detailRequest.current) return;
      setComments((current) => [...current, ...page.items]);
      setCommentCursor(page.nextCursor ?? '');
    } catch (requestError) {
      if (request === detailRequest.current) setError(messageOf(requestError, '评论加载失败'));
    }
  }

  if (selectedPost) {
    const ownsPost = user?.id === selectedPost.author.id;
    return (
      <section className="page-stage community-stage">
        <button className="text-button" type="button" onClick={closePost}>← 返回社区</button>
        {error ? <div className="error-banner">{error}</div> : null}
        <article className="community-detail">
          {editingPost ? (
            <form className="community-form" onSubmit={savePost}>
              <PostFields games={games} gameId={postGameId} title={title} content={content} onGameId={setPostGameId} onTitle={setTitle} onContent={setContent} />
              <div className="row-actions"><button type="submit" disabled={loading}>保存</button><button type="button" onClick={() => setEditingPost(false)}>取消</button></div>
            </form>
          ) : (
            <>
              <div className="community-meta">{selectedPost.game?.name ?? '综合讨论'} · {selectedPost.author.displayName} · {formatDate(selectedPost.createdAt)}</div>
              <h1>{selectedPost.title}</h1>
              <p className="community-content">{selectedPost.content}</p>
              {ownsPost ? <div className="row-actions"><button type="button" onClick={beginPostEdit}>编辑</button><button type="button" onClick={() => void removePost()}>删除</button></div> : null}
            </>
          )}
          <div className="reaction-row" aria-label="帖子反应">
            {reactions.map(({ type, label }) => <button className={selectedPost.viewerReactions.includes(type) ? 'active' : ''} type="button" key={type} onClick={() => void toggleReaction(type)}>{label} {selectedPost.reactionCounts[type] ?? 0}</button>)}
          </div>
        </article>

        <section className="comment-section">
          <h2>评论 {selectedPost.commentCount}</h2>
          <form className="comment-form" onSubmit={submitComment}>
            <label htmlFor="community-comment">写评论</label>
            <div><input id="community-comment" value={comment} onChange={(event) => setComment(event.target.value)} placeholder={user ? '分享你的看法' : '登录后参与讨论'} required />{user ? <button type="submit" disabled={loading}>{loading ? '发布中…' : '发布'}</button> : <button type="button" onClick={onRequireLogin}>登录后发布</button>}</div>
          </form>
          {comments.length === 0 ? <p className="empty-state compact">还没有评论。</p> : comments.map((item) => (
            <article className="comment-card" key={item.id}>
              <div className="community-meta">{item.author.displayName} · {formatDate(item.createdAt)}</div>
              {editingComment === item.id ? <div className="comment-edit"><input value={editingCommentContent} onChange={(event) => setEditingCommentContent(event.target.value)} /><button type="button" onClick={() => void saveComment(item.id)}>保存</button><button type="button" onClick={() => setEditingComment(null)}>取消</button></div> : <p>{item.content}</p>}
              {user?.id === item.author.id && editingComment !== item.id ? <div className="row-actions"><button type="button" onClick={() => { setEditingComment(item.id); setEditingCommentContent(item.content); }}>编辑</button><button type="button" onClick={() => void removeComment(item.id)}>删除</button></div> : null}
            </article>
          ))}
          {commentCursor ? <button className="load-more" type="button" onClick={() => void loadMoreComments()}>加载更多评论</button> : null}
        </section>
      </section>
    );
  }

  return (
    <section className="page-stage community-stage">
      <div className="community-heading"><div><h1>游戏社区</h1><p>攻略、体验和版本讨论。</p></div></div>
      <form className="community-filter" onSubmit={(event) => { event.preventDefault(); void loadPosts('', false); }}>
        <label htmlFor="community-game-filter">筛选游戏</label>
        <select id="community-game-filter" value={gameId} onChange={(event) => setGameId(event.target.value)}><option value="">全部游戏</option>{games.map((game) => <option key={game.id} value={game.id}>{game.name}</option>)}</select>
        <button type="submit">筛选</button>
      </form>
      <form className="community-form" onSubmit={submitPost}>
        <h2>发布帖子</h2>
        <PostFields games={games} gameId={postGameId} title={title} content={content} onGameId={setPostGameId} onTitle={setTitle} onContent={setContent} />
        {user ? <button type="submit" disabled={loading}>发布</button> : <button type="button" onClick={onRequireLogin}>登录后发布</button>}
      </form>
      {error ? <div className="error-banner">{error}</div> : null}
      {loading && posts.length === 0 ? <p className="empty-state">正在加载社区…</p> : posts.length === 0 ? <p className="empty-state">还没有帖子，来发布第一篇吧。</p> : (
        <div className="community-feed">{posts.map((post) => (
          <article className="community-card" key={post.id}>
            <button type="button" onClick={() => void openPost(post.id)}><div className="community-meta">{post.game?.name ?? '综合讨论'} · {post.author.displayName} · {formatDate(post.createdAt)}</div><h2>{post.title}</h2><p>{post.content}</p></button>
            <div className="community-stats"><span>评论 {post.commentCount}</span><span>赞 {post.reactionCounts.like ?? 0}</span><span>有帮助 {post.reactionCounts.helpful ?? 0}</span></div>
          </article>
        ))}</div>
      )}
      {nextCursor ? <button className="load-more" type="button" onClick={() => void loadPosts(nextCursor, true)}>加载更多帖子</button> : null}
    </section>
  );
}

function PostFields({ games, gameId, title, content, onGameId, onTitle, onContent }: { games: Game[]; gameId: string; title: string; content: string; onGameId: (value: string) => void; onTitle: (value: string) => void; onContent: (value: string) => void }) {
  return <><label>关联游戏<select value={gameId} onChange={(event) => onGameId(event.target.value)}><option value="">综合讨论</option>{games.map((game) => <option key={game.id} value={game.id}>{game.name}</option>)}</select></label><label>标题<input value={title} onChange={(event) => onTitle(event.target.value)} maxLength={160} required /></label><label>内容<textarea value={content} onChange={(event) => onContent(event.target.value)} maxLength={10000} rows={5} required /></label></>;
}

function formatDate(value: string) {
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium' }).format(new Date(value));
}

function messageOf(error: unknown, fallback: string) {
  return error instanceof Error ? error.message : fallback;
}
