import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import AccountSettings from './AccountSettings';

const api = vi.hoisted(() => ({
  clearAssistantProfile: vi.fn(),
  createKnowledgeDocument: vi.fn(),
  deleteKnowledgeDocument: vi.fn(),
  getAssistantProfile: vi.fn(),
  getKnowledgeTrack: vi.fn(),
  listKnowledgeDocuments: vi.fn(),
  replaceAssistantProfile: vi.fn()
}));
vi.mock('../lib/api', () => api);

beforeEach(() => {
  api.getAssistantProfile.mockResolvedValue({ favoriteGenres: ['rpg'], preferredPlatforms: ['pc'], defaultRegion: 'CN', preferredLanguages: ['zh-CN'], maxPriceMinor: 30000, currency: 'CNY' });
  api.listKnowledgeDocuments.mockResolvedValue({ items: [], page: 1, pageSize: 20, totalCount: 0, totalPages: 0 });
});
afterEach(() => { cleanup(); vi.clearAllMocks(); vi.useRealTimers(); });

describe('AccountSettings', () => {
  it('loads and replaces the authenticated user profile', async () => {
    api.replaceAssistantProfile.mockImplementation(async (profile) => profile);
    render(<AccountSettings user={{ id: '7', username: 'player', displayName: 'Player', role: 'user' }} authBusy={false} onSignOut={vi.fn()} />);
    expect(await screen.findByDisplayValue('rpg')).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('喜欢的类型'), { target: { value: 'rpg, strategy' } });
    fireEvent.click(screen.getByRole('button', { name: '保存偏好' }));
    await waitFor(() => expect(api.replaceAssistantProfile).toHaveBeenCalledWith(expect.objectContaining({ favoriteGenres: ['rpg', 'strategy'], defaultRegion: 'CN' })));
    expect(await screen.findByText('助手偏好已保存。')).toBeInTheDocument();
    expect(api.listKnowledgeDocuments).not.toHaveBeenCalled();
  });

  it('clears only the assistant profile', async () => {
    api.clearAssistantProfile.mockResolvedValue(undefined);
    render(<AccountSettings user={{ id: '7', username: 'player', displayName: 'Player', role: 'user' }} authBusy={false} onSignOut={vi.fn()} />);
    await screen.findByDisplayValue('rpg');
    fireEvent.click(screen.getByRole('button', { name: '清空偏好' }));
    await waitFor(() => expect(api.clearAssistantProfile).toHaveBeenCalledOnce());
    expect(screen.getByLabelText('喜欢的类型')).toHaveValue('');
  });

  it('shows async knowledge management only to admins', async () => {
    api.createKnowledgeDocument.mockResolvedValue({ trackId: 'track-1', sourceKey: 'xlh-source.txt', status: 'accepted' });
    api.getKnowledgeTrack.mockResolvedValue({ trackId: 'track-1', documents: [{ documentId: 'doc-1', sourceKey: 'xlh-source.txt', status: 'PROCESSED', contentLength: 4, chunksCount: 1 }], totalCount: 1, statusCounts: { PROCESSED: 1 } });
    api.listKnowledgeDocuments.mockResolvedValue({ items: [{ documentId: 'doc-1', sourceKey: 'xlh-source.txt', status: 'PROCESSED', contentLength: 4, chunksCount: 1 }], page: 1, pageSize: 20, totalCount: 1, totalPages: 1 });
    render(<AccountSettings user={{ id: '1', username: 'admin', displayName: 'Admin', role: 'admin' }} authBusy={false} onSignOut={vi.fn()} />);
    expect(await screen.findByText('LightRAG 知识管理')).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('知识标题'), { target: { value: 'Guide' } });
    fireEvent.change(screen.getByLabelText('知识正文'), { target: { value: 'body' } });
    fireEvent.click(screen.getByRole('button', { name: '提交并跟踪索引' }));
    await act(async () => { await Promise.resolve(); });
    await waitFor(() => expect(api.createKnowledgeDocument).toHaveBeenCalledOnce());
    expect(api.getKnowledgeTrack).toHaveBeenCalledWith('track-1', expect.any(AbortSignal));
    await waitFor(() => expect(screen.getByText('知识文档索引完成。')).toBeInTheDocument());
  });

  it('paginates managed LightRAG documents', async () => {
    api.listKnowledgeDocuments
      .mockResolvedValueOnce({ items: [{ documentId: 'doc-1', sourceKey: 'xlh-one.txt', status: 'PROCESSED', contentLength: 4, chunksCount: 1 }], page: 1, pageSize: 20, totalCount: 21, totalPages: 2 })
      .mockResolvedValueOnce({ items: [{ documentId: 'doc-2', sourceKey: 'xlh-two.txt', status: 'PROCESSED', contentLength: 5, chunksCount: 1 }], page: 2, pageSize: 20, totalCount: 21, totalPages: 2 });
    render(<AccountSettings user={{ id: '1', username: 'admin', displayName: 'Admin', role: 'admin' }} authBusy={false} onSignOut={vi.fn()} />);
    expect(await screen.findByText('xlh-one.txt')).toBeInTheDocument();
    expect(screen.getByText('第 1 / 2 页，共 21 份')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    expect(await screen.findByText('xlh-two.txt')).toBeInTheDocument();
    expect(api.listKnowledgeDocuments).toHaveBeenLastCalledWith(2);
    expect(screen.getByRole('button', { name: '下一页' })).toBeDisabled();
  });

  it('explains when advanced LightRAG routes are disabled', async () => {
    api.listKnowledgeDocuments.mockRejectedValue({ status: 404 });
    render(<AccountSettings user={{ id: '1', username: 'admin', displayName: 'Admin', role: 'admin' }} authBusy={false} onSignOut={vi.fn()} />);
    expect(await screen.findByText('当前部署未启用高级 AI / LightRAG，知识管理功能不可用。')).toBeInTheDocument();
    expect(screen.queryByLabelText('知识标题')).not.toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });
});
