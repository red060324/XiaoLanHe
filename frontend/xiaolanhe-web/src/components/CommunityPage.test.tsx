import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import CommunityPage from './CommunityPage';

const api = vi.hoisted(() => ({
  createCommunityComment: vi.fn(),
  createCommunityPost: vi.fn(),
  deleteCommunityComment: vi.fn(),
  deleteCommunityPost: vi.fn(),
  getCommunityPost: vi.fn(),
  listCommunityComments: vi.fn(),
  listCommunityPosts: vi.fn(),
  setCommunityReaction: vi.fn(),
  updateCommunityComment: vi.fn(),
  updateCommunityPost: vi.fn()
}));

vi.mock('../lib/api', () => api);

const user = { id: '7', username: 'player', displayName: 'Player', role: 'user' as const };
const post = {
  id: '9',
  title: 'Boss Guide',
  content: 'Use frost damage.',
  status: 'published' as const,
  author: user,
  commentCount: 0,
  reactionCounts: { like: 0, helpful: 0, funny: 0 },
  viewerReactions: [],
  createdAt: '2026-08-31T00:00:00Z',
  updatedAt: '2026-08-31T00:00:00Z'
};

beforeEach(() => {
  api.listCommunityPosts.mockResolvedValue({ items: [post] });
  api.getCommunityPost.mockResolvedValue(post);
  api.listCommunityComments.mockResolvedValue({ items: [] });
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('CommunityPage', () => {
  it('opens a post and updates comments and reactions', async () => {
    api.createCommunityComment.mockResolvedValue({
      id: '11',
      postId: post.id,
      content: 'That worked.',
      status: 'published',
      author: user,
      createdAt: post.createdAt,
      updatedAt: post.updatedAt
    });
    api.setCommunityReaction.mockResolvedValue({
      reactionCounts: { like: 1, helpful: 0, funny: 0 },
      viewerReactions: ['like']
    });

    render(<CommunityPage user={user} games={[]} onRequireLogin={vi.fn()} />);
    fireEvent.click(await screen.findByRole('button', { name: /Boss Guide/ }));
    expect(await screen.findByRole('heading', { name: 'Boss Guide', level: 1 })).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText('写评论'), { target: { value: 'That worked.' } });
    fireEvent.click(screen.getByRole('button', { name: '发布' }));
    expect(await screen.findByText('That worked.')).toBeInTheDocument();
    expect(api.createCommunityComment).toHaveBeenCalledWith('9', 'That worked.');

    fireEvent.click(screen.getByRole('button', { name: '赞 0' }));
    await waitFor(() => expect(screen.getByRole('button', { name: '赞 1' })).toHaveClass('active'));
    expect(api.setCommunityReaction).toHaveBeenCalledWith('9', 'like', true);
  });

  it('keeps a successfully created post open when a follow-up read is unavailable', async () => {
    api.listCommunityPosts.mockResolvedValue({ items: [] });
    api.createCommunityPost.mockResolvedValue(post);
    api.getCommunityPost.mockRejectedValue(new Error('detail unavailable'));

    render(<CommunityPage user={user} games={[]} onRequireLogin={vi.fn()} />);
    await screen.findByText('还没有帖子，来发布第一篇吧。');
    fireEvent.change(screen.getByLabelText('标题'), { target: { value: post.title } });
    fireEvent.change(screen.getByLabelText('内容'), { target: { value: post.content } });
    fireEvent.click(screen.getByRole('button', { name: '发布' }));

    expect(await screen.findByRole('heading', { name: post.title })).toBeInTheDocument();
    expect(screen.queryByText('detail unavailable')).not.toBeInTheDocument();
  });

  it('does not reopen a post when an earlier edit finishes after returning to the feed', async () => {
    let resolveUpdate!: (value: unknown) => void;
    api.updateCommunityPost.mockReturnValue(new Promise((resolve) => { resolveUpdate = resolve; }));
    render(<CommunityPage user={user} games={[]} onRequireLogin={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: /Boss Guide/ }));
    await screen.findByRole('heading', { name: 'Boss Guide', level: 1 });
    fireEvent.click(screen.getByRole('button', { name: '编辑' }));
    fireEvent.click(screen.getByRole('button', { name: '保存' }));
    await waitFor(() => expect(api.updateCommunityPost).toHaveBeenCalledOnce());
    fireEvent.click(screen.getByRole('button', { name: '← 返回社区' }));

    await act(async () => resolveUpdate({ ...post, title: 'Updated Guide' }));

    expect(screen.getByRole('heading', { name: '游戏社区', level: 1 })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Updated Guide', level: 2 })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Updated Guide', level: 1 })).not.toBeInTheDocument();
  });

  it('sends a guest to login when they choose to publish', async () => {
    const onRequireLogin = vi.fn();
    api.listCommunityPosts.mockResolvedValue({ items: [] });

    render(<CommunityPage user={null} games={[]} onRequireLogin={onRequireLogin} />);
    await screen.findByText('还没有帖子，来发布第一篇吧。');
    fireEvent.click(screen.getByRole('button', { name: '登录后发布' }));

    expect(onRequireLogin).toHaveBeenCalledOnce();
  });

  it('keeps the latest game filter when the initial feed load finishes later', async () => {
    let resolveInitial!: (value: unknown) => void;
    let resolveFiltered!: (value: unknown) => void;
    api.listCommunityPosts
      .mockReturnValueOnce(new Promise((resolve) => { resolveInitial = resolve; }))
      .mockReturnValueOnce(new Promise((resolve) => { resolveFiltered = resolve; }));
    render(<CommunityPage user={user} games={[{ id: '3', slug: 'demo', name: 'Demo', summary: '', owned: false }]} onRequireLogin={vi.fn()} />);

    await waitFor(() => expect(api.listCommunityPosts).toHaveBeenCalledWith('', ''));
    fireEvent.change(screen.getByLabelText('筛选游戏'), { target: { value: '3' } });
    fireEvent.click(screen.getByRole('button', { name: '筛选' }));
    await waitFor(() => expect(api.listCommunityPosts).toHaveBeenCalledWith('3', ''));

    await act(async () => resolveFiltered({ items: [{ ...post, id: '10', title: 'Filtered Guide' }] }));
    expect(await screen.findByText('Filtered Guide')).toBeInTheDocument();

    await act(async () => resolveInitial({ items: [{ ...post, title: 'Old Feed' }] }));
    expect(screen.getByText('Filtered Guide')).toBeInTheDocument();
    expect(screen.queryByText('Old Feed')).not.toBeInTheDocument();
  });

  it('keeps the latest selected post when an earlier detail request finishes later', async () => {
    const secondPost = { ...post, id: '10', title: 'Second Guide' };
    let resolveFirst!: (value: unknown) => void;
    let resolveSecond!: (value: unknown) => void;
    api.listCommunityPosts.mockResolvedValue({ items: [post, secondPost] });
    api.getCommunityPost
      .mockReturnValueOnce(new Promise((resolve) => { resolveFirst = resolve; }))
      .mockReturnValueOnce(new Promise((resolve) => { resolveSecond = resolve; }));
    render(<CommunityPage user={user} games={[]} onRequireLogin={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: /Boss Guide/ }));
    fireEvent.click(screen.getByRole('button', { name: /Second Guide/ }));
    await waitFor(() => expect(api.getCommunityPost).toHaveBeenCalledTimes(2));

    await act(async () => resolveSecond(secondPost));
    expect(await screen.findByRole('heading', { name: 'Second Guide', level: 1 })).toBeInTheDocument();

    await act(async () => resolveFirst(post));
    expect(screen.getByRole('heading', { name: 'Second Guide', level: 1 })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Boss Guide', level: 1 })).not.toBeInTheDocument();
  });

  it('submits one comment while the first request is still pending', async () => {
    let resolveComment!: (value: unknown) => void;
    api.createCommunityComment.mockReturnValue(new Promise((resolve) => { resolveComment = resolve; }));
    render(<CommunityPage user={user} games={[]} onRequireLogin={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: /Boss Guide/ }));
    await screen.findByRole('heading', { name: 'Boss Guide', level: 1 });
    fireEvent.change(screen.getByLabelText('写评论'), { target: { value: 'Only once.' } });
    const form = screen.getByLabelText('写评论').closest('form')!;
    fireEvent.submit(form);
    fireEvent.submit(form);

    expect(api.createCommunityComment).toHaveBeenCalledOnce();

    await act(async () => resolveComment({
      id: '11',
      postId: post.id,
      content: 'Only once.',
      status: 'published',
      author: user,
      createdAt: post.createdAt,
      updatedAt: post.updatedAt
    }));
  });

  it('keeps a submitted comment with its original post when another post opens', async () => {
    const secondPost = { ...post, id: '10', title: 'Second Guide', commentCount: 4 };
    const createdComment = { id: '11', postId: post.id, content: 'Only for the first post', status: 'published' as const, author: user, createdAt: post.createdAt, updatedAt: post.updatedAt };
    let resolveComment!: (value: unknown) => void;
    api.listCommunityPosts.mockResolvedValue({ items: [post, secondPost] });
    api.getCommunityPost.mockResolvedValueOnce(post).mockResolvedValueOnce(secondPost);
    api.createCommunityComment.mockReturnValue(new Promise((resolve) => { resolveComment = resolve; }));
    render(<CommunityPage user={user} games={[]} onRequireLogin={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: /Boss Guide/ }));
    await screen.findByRole('heading', { name: 'Boss Guide', level: 1 });
    fireEvent.change(screen.getByLabelText('写评论'), { target: { value: createdComment.content } });
    fireEvent.click(screen.getByRole('button', { name: '发布' }));
    await waitFor(() => expect(api.createCommunityComment).toHaveBeenCalledOnce());

    fireEvent.click(screen.getByRole('button', { name: '← 返回社区' }));
    fireEvent.click(screen.getByRole('button', { name: /Second Guide/ }));
    expect(await screen.findByRole('heading', { name: 'Second Guide', level: 1 })).toBeInTheDocument();

    await act(async () => resolveComment(createdComment));
    expect(screen.queryByText(createdComment.content)).not.toBeInTheDocument();
    expect(screen.getByRole('heading', { name: '评论 4', level: 2 })).toBeInTheDocument();
  });

  it('ignores an older comment page after another post opens', async () => {
    const secondPost = { ...post, id: '10', title: 'Second Guide' };
    const firstComment = { id: '11', postId: post.id, content: 'First comment', status: 'published' as const, author: user, createdAt: post.createdAt, updatedAt: post.updatedAt };
    const olderPageComment = { ...firstComment, id: '12', content: 'Older page comment' };
    const secondComment = { ...firstComment, id: '13', postId: secondPost.id, content: 'Second post comment' };
    let resolveOlderPage!: (value: unknown) => void;
    api.listCommunityPosts.mockResolvedValue({ items: [post, secondPost] });
    api.getCommunityPost.mockResolvedValueOnce(post).mockResolvedValueOnce(secondPost);
    api.listCommunityComments
      .mockResolvedValueOnce({ items: [firstComment], nextCursor: 'next-a' })
      .mockReturnValueOnce(new Promise((resolve) => { resolveOlderPage = resolve; }))
      .mockResolvedValueOnce({ items: [secondComment] });
    render(<CommunityPage user={user} games={[]} onRequireLogin={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: /Boss Guide/ }));
    expect(await screen.findByText(firstComment.content)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '加载更多评论' }));
    await waitFor(() => expect(api.listCommunityComments).toHaveBeenCalledTimes(2));

    fireEvent.click(screen.getByRole('button', { name: '← 返回社区' }));
    fireEvent.click(screen.getByRole('button', { name: /Second Guide/ }));
    expect(await screen.findByText(secondComment.content)).toBeInTheDocument();

    await act(async () => resolveOlderPage({ items: [olderPageComment] }));
    expect(screen.getByText(secondComment.content)).toBeInTheDocument();
    expect(screen.queryByText(olderPageComment.content)).not.toBeInTheDocument();
  });

  it('keeps the community feed open when an older reaction completes', async () => {
    let resolveReaction!: (value: unknown) => void;
    api.setCommunityReaction.mockReturnValue(new Promise((resolve) => { resolveReaction = resolve; }));
    render(<CommunityPage user={user} games={[]} onRequireLogin={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: /Boss Guide/ }));
    await screen.findByRole('heading', { name: 'Boss Guide', level: 1 });
    fireEvent.click(screen.getByRole('button', { name: '赞 0' }));
    await waitFor(() => expect(api.setCommunityReaction).toHaveBeenCalledOnce());
    fireEvent.click(screen.getByRole('button', { name: '← 返回社区' }));
    expect(await screen.findByRole('heading', { name: '游戏社区', level: 1 })).toBeInTheDocument();

    await act(async () => resolveReaction({ reactionCounts: { like: 1, helpful: 0, funny: 0 }, viewerReactions: ['like'] }));
    expect(screen.getByRole('heading', { name: '游戏社区', level: 1 })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Boss Guide', level: 1 })).not.toBeInTheDocument();
  });
});
