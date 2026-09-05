import { FormEvent, useEffect, useRef, useState } from 'react';
import {
  AssistantProfile, KnowledgeDocument, KnowledgeDocumentDraft, KnowledgeDocumentPage, User, clearAssistantProfile,
  createKnowledgeDocument, deleteKnowledgeDocument, getAssistantProfile, getKnowledgeTrack,
  listKnowledgeDocuments, replaceAssistantProfile
} from '../lib/api';

type Props = {
  user: User;
  authBusy: boolean;
  onSignOut: () => void;
};

const emptyProfile: AssistantProfile = { favoriteGenres: [], preferredPlatforms: [], defaultRegion: '', preferredLanguages: [] };
const emptyDocument: KnowledgeDocumentDraft = { sourceType: 'guide', title: '', sourceUrl: '', gameCode: '', regionCode: '', patchVersion: '', contentText: '' };
const terminalKnowledgeStatuses = new Set(['PROCESSED', 'FAILED']);
const emptyKnowledgePage: KnowledgeDocumentPage = { items: [], page: 1, pageSize: 20, totalCount: 0, totalPages: 0 };

function values(text: string): string[] {
  return text.split(',').map((value) => value.trim()).filter(Boolean);
}

function message(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback;
}

function hasStatus(error: unknown, status: number): boolean {
  return typeof error === 'object' && error !== null && 'status' in error && (error as { status?: unknown }).status === status;
}

function waitForPoll(signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = window.setTimeout(resolve, 1000);
    signal.addEventListener('abort', () => {
      window.clearTimeout(timer);
      reject(new DOMException('Aborted', 'AbortError'));
    }, { once: true });
  });
}

export default function AccountSettings({ user, authBusy, onSignOut }: Props) {
  const [profile, setProfile] = useState<AssistantProfile>(emptyProfile);
  const [profileBusy, setProfileBusy] = useState(false);
  const [profileNotice, setProfileNotice] = useState('');
  const [profileError, setProfileError] = useState('');
  const [draft, setDraft] = useState<KnowledgeDocumentDraft>(emptyDocument);
  const [documents, setDocuments] = useState<KnowledgeDocument[]>([]);
  const [documentPage, setDocumentPage] = useState<KnowledgeDocumentPage>(emptyKnowledgePage);
  const [knowledgeAvailable, setKnowledgeAvailable] = useState<boolean | null>(null);
  const [knowledgeBusy, setKnowledgeBusy] = useState(false);
  const [knowledgeNotice, setKnowledgeNotice] = useState('');
  const [knowledgeError, setKnowledgeError] = useState('');
  const generation = useRef(0);
  const pollController = useRef<AbortController | null>(null);

  useEffect(() => {
    const current = ++generation.current;
    setProfileBusy(true);
    setProfileError('');
    void getAssistantProfile().then((value) => {
      if (generation.current === current) setProfile(value);
    }).catch((error) => {
      if (generation.current === current) setProfileError(message(error, '助手偏好加载失败'));
    }).finally(() => {
      if (generation.current === current) setProfileBusy(false);
    });
    if (user.role === 'admin') void refreshDocuments(1, current);
    return () => {
      generation.current++;
      pollController.current?.abort();
    };
  }, [user.id, user.role]);

  async function refreshDocuments(pageNumber = documentPage.page, current = generation.current) {
    setKnowledgeBusy(true);
    setKnowledgeError('');
    try {
      let page = await listKnowledgeDocuments(pageNumber);
      if (pageNumber > 1 && page.items.length === 0 && page.totalPages < pageNumber) {
        page = await listKnowledgeDocuments(Math.max(1, page.totalPages));
      }
      if (generation.current === current) {
        setDocuments(page.items);
        setDocumentPage(page);
        setKnowledgeAvailable(true);
      }
    } catch (error) {
      if (generation.current === current) {
        if (hasStatus(error, 404)) {
          setDocuments([]);
          setDocumentPage(emptyKnowledgePage);
          setKnowledgeAvailable(false);
          setKnowledgeError('');
        } else {
          setKnowledgeAvailable(null);
          setKnowledgeError(message(error, '知识文档加载失败'));
        }
      }
    } finally {
      if (generation.current === current) setKnowledgeBusy(false);
    }
  }

  async function saveProfile(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setProfileBusy(true);
    setProfileError('');
    setProfileNotice('');
    try {
      const saved = await replaceAssistantProfile(profile);
      setProfile(saved);
      setProfileNotice('助手偏好已保存。');
    } catch (error) {
      setProfileError(message(error, '助手偏好保存失败'));
    } finally {
      setProfileBusy(false);
    }
  }

  async function clearProfile() {
    setProfileBusy(true);
    setProfileError('');
    setProfileNotice('');
    try {
      await clearAssistantProfile();
      setProfile(emptyProfile);
      setProfileNotice('助手偏好已清空。');
    } catch (error) {
      setProfileError(message(error, '助手偏好清空失败'));
    } finally {
      setProfileBusy(false);
    }
  }

  async function pollTrack(trackId: string, requestGeneration: number) {
    const controller = new AbortController();
    pollController.current?.abort();
    pollController.current = controller;
    for (let attempt = 0; attempt < 60; attempt++) {
      if (attempt > 0) await waitForPoll(controller.signal);
      const track = await getKnowledgeTrack(trackId, controller.signal);
      if (generation.current !== requestGeneration || controller.signal.aborted) return;
      const statuses = track.documents.map((document) => document.status.toUpperCase());
      if (statuses.some((status) => status === 'FAILED')) throw new Error('LightRAG 索引失败');
      if (statuses.length > 0 && statuses.every((status) => terminalKnowledgeStatuses.has(status))) {
        setKnowledgeNotice('知识文档索引完成。');
        await refreshDocuments(1, requestGeneration);
        return;
      }
    }
    setKnowledgeNotice('索引仍在进行，可稍后刷新文档列表。');
  }

  async function submitKnowledge(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const current = generation.current;
    setKnowledgeBusy(true);
    setKnowledgeError('');
    setKnowledgeNotice('');
    try {
      const accepted = await createKnowledgeDocument(draft);
      if (generation.current !== current) return;
      setKnowledgeNotice(accepted.replayed ? '已找到同一文档，继续跟踪索引。' : '文档已接收，正在建立索引。');
      setDraft(emptyDocument);
      await pollTrack(accepted.trackId, current);
    } catch (error) {
      if (generation.current === current && !(error instanceof DOMException && error.name === 'AbortError')) setKnowledgeError(message(error, '知识文档提交失败'));
    } finally {
      if (generation.current === current) setKnowledgeBusy(false);
    }
  }

  async function removeDocument(documentId: string) {
    if (!window.confirm('确认删除这份知识文档？删除完成前可能仍可被检索。')) return;
    setKnowledgeBusy(true);
    setKnowledgeError('');
    try {
      await deleteKnowledgeDocument(documentId);
      setKnowledgeNotice('删除已提交，请稍后刷新确认。');
      await refreshDocuments(documentPage.page);
    } catch (error) {
      setKnowledgeError(message(error, '知识文档删除失败'));
    } finally {
      setKnowledgeBusy(false);
    }
  }

  return (
    <section className="settings-stack">
      <article className="settings-card account-summary"><div><h1>{user.displayName}</h1><p>@{user.username} · {user.role}</p></div><button type="button" disabled={authBusy} onClick={onSignOut}>退出登录</button></article>
      <form className="settings-card settings-form" onSubmit={saveProfile} aria-busy={profileBusy}>
        <div><h2>助手偏好</h2><p>只使用你主动填写的偏好，不从聊天内容猜测身份或权限。</p></div>
        <label>喜欢的类型<input aria-label="喜欢的类型" value={profile.favoriteGenres.join(', ')} onChange={(event) => setProfile({ ...profile, favoriteGenres: values(event.target.value) })} placeholder="rpg, strategy" /></label>
        <label>常用平台<input aria-label="常用平台" value={profile.preferredPlatforms.join(', ')} onChange={(event) => setProfile({ ...profile, preferredPlatforms: values(event.target.value) })} placeholder="pc, ps5" /></label>
        <label>首选语言<input aria-label="首选语言" value={profile.preferredLanguages.join(', ')} onChange={(event) => setProfile({ ...profile, preferredLanguages: values(event.target.value) })} placeholder="zh-CN, en-US" /></label>
        <div className="settings-grid"><label>默认地区<input aria-label="默认地区" value={profile.defaultRegion} onChange={(event) => setProfile({ ...profile, defaultRegion: event.target.value.toUpperCase() })} placeholder="CN" /></label><label>货币<input aria-label="货币" value={profile.currency ?? ''} onChange={(event) => setProfile({ ...profile, currency: event.target.value.toUpperCase() || undefined })} placeholder="CNY" /></label><label>最高价格（分）<input aria-label="最高价格（分）" type="number" min="1" value={profile.maxPriceMinor ?? ''} onChange={(event) => setProfile({ ...profile, maxPriceMinor: event.target.value ? Number(event.target.value) : undefined })} /></label></div>
        <div className="settings-actions"><button type="submit" disabled={profileBusy}>保存偏好</button><button className="outline-button" type="button" disabled={profileBusy} onClick={() => void clearProfile()}>清空偏好</button></div>
        {profileNotice ? <p className="success-banner" role="status">{profileNotice}</p> : null}{profileError ? <p className="error-banner" role="alert">{profileError}</p> : null}
      </form>
      {user.role === 'admin' ? <section className="settings-card knowledge-admin"><div><h2>LightRAG 知识管理</h2><p>文档由官方 LightRAG 异步索引；这里只显示小蓝盒管理的 source。</p></div>{knowledgeAvailable === false ? <p className="knowledge-disabled" role="status">当前部署未启用高级 AI / LightRAG，知识管理功能不可用。</p> : knowledgeAvailable === null && knowledgeBusy ? <p role="status">正在检查 LightRAG 功能…</p> : <><form className="settings-form" onSubmit={submitKnowledge}><label>标题<input aria-label="知识标题" required maxLength={512} value={draft.title} onChange={(event) => setDraft({ ...draft, title: event.target.value })} /></label><div className="settings-grid"><label>来源类型<input aria-label="来源类型" required maxLength={32} value={draft.sourceType} onChange={(event) => setDraft({ ...draft, sourceType: event.target.value })} /></label><label>游戏标识<input aria-label="游戏标识" maxLength={64} value={draft.gameCode} onChange={(event) => setDraft({ ...draft, gameCode: event.target.value })} /></label><label>地区<input aria-label="知识地区" maxLength={32} value={draft.regionCode} onChange={(event) => setDraft({ ...draft, regionCode: event.target.value.toUpperCase() })} /></label><label>版本<input aria-label="补丁版本" maxLength={64} value={draft.patchVersion} onChange={(event) => setDraft({ ...draft, patchVersion: event.target.value })} /></label></div><label>来源 URL<input aria-label="来源 URL" type="url" maxLength={2048} value={draft.sourceUrl} onChange={(event) => setDraft({ ...draft, sourceUrl: event.target.value })} /></label><label>正文<textarea aria-label="知识正文" required maxLength={1048576} rows={8} value={draft.contentText} onChange={(event) => setDraft({ ...draft, contentText: event.target.value })} /></label><div className="settings-actions"><button type="submit" disabled={knowledgeBusy}>提交并跟踪索引</button><button className="outline-button" type="button" disabled={knowledgeBusy} onClick={() => void refreshDocuments(documentPage.page)}>刷新列表</button></div></form>{knowledgeNotice ? <p className="success-banner" role="status">{knowledgeNotice}</p> : null}{knowledgeError ? <p className="error-banner" role="alert">{knowledgeError}</p> : null}<div className="knowledge-list">{documents.length === 0 ? <p>暂无已管理文档。</p> : documents.map((document) => <article key={document.documentId}><div><strong>{document.sourceKey}</strong><span>{document.status} · {document.chunksCount} chunks · {document.contentLength} chars</span></div><button type="button" disabled={knowledgeBusy} onClick={() => void removeDocument(document.documentId)}>删除</button></article>)}</div><nav className="knowledge-pagination" aria-label="知识文档分页"><button className="outline-button" type="button" disabled={knowledgeBusy || documentPage.page <= 1} onClick={() => void refreshDocuments(documentPage.page - 1)}>上一页</button><span>第 {documentPage.page} / {Math.max(1, documentPage.totalPages)} 页，共 {documentPage.totalCount} 份</span><button className="outline-button" type="button" disabled={knowledgeBusy || documentPage.totalPages === 0 || documentPage.page >= documentPage.totalPages} onClick={() => void refreshDocuments(documentPage.page + 1)}>下一页</button></nav></>}</section> : null}
    </section>
  );
}
