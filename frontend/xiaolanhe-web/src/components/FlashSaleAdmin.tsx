import { FormEvent, useEffect, useRef, useState } from 'react';
import {
  AdminFlashSale, FlashSaleDraft, activateAdminFlashSale, cancelAdminFlashSale,
  createAdminFlashSale, getAdminFlashSale, listAdminFlashSales, updateAdminFlashSale
} from '../lib/api';

type Props = { ownerId: string };

type DraftForm = {
  code: string;
  editionId: string;
  region: string;
  currency: string;
  salePriceMinor: string;
  totalStock: string;
  startsAt: string;
  endsAt: string;
  paymentTimeoutSeconds: string;
};

const emptyDraft: DraftForm = {
  code: '',
  editionId: '',
  region: 'GLOBAL',
  currency: 'USD',
  salePriceMinor: '',
  totalStock: '',
  startsAt: '',
  endsAt: '',
  paymentTimeoutSeconds: '900'
};

const statusLabels: Record<AdminFlashSale['status'], string> = {
  draft: '草稿',
  active: '已激活',
  ended: '已结束',
  cancelled: '已取消'
};

function message(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback;
}

function hasStatus(error: unknown, status: number): boolean {
  return typeof error === 'object' && error !== null && 'status' in error && (error as { status?: unknown }).status === status;
}

function localDateTime(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  const pad = (part: number) => String(part).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}

function parsedLocalDateTime(value: string): Date | null {
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/.exec(value);
  if (!match) return null;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return null;
  const [, year, month, day, hour, minute, second = '00'] = match;
  if (date.getFullYear() !== Number(year) || date.getMonth() + 1 !== Number(month) || date.getDate() !== Number(day) ||
    date.getHours() !== Number(hour) || date.getMinutes() !== Number(minute) || date.getSeconds() !== Number(second)) return null;
  return date;
}

function formFromActivity(activity: AdminFlashSale): DraftForm {
  return {
    code: activity.code,
    editionId: activity.editionId,
    region: activity.region,
    currency: activity.currency,
    salePriceMinor: String(activity.salePriceMinor),
    totalStock: String(activity.totalStock),
    startsAt: localDateTime(activity.startsAt),
    endsAt: localDateTime(activity.endsAt),
    paymentTimeoutSeconds: String(activity.paymentTimeoutSeconds)
  };
}

function payloadFromForm(form: DraftForm): FlashSaleDraft {
  const startsAt = parsedLocalDateTime(form.startsAt);
  const endsAt = parsedLocalDateTime(form.endsAt);
  const editionIDIsValid = /^[1-9]\d*$/.test(form.editionId.trim());
  const salePriceMinor = Number(form.salePriceMinor);
  const totalStock = Number(form.totalStock);
  const paymentTimeoutSeconds = Number(form.paymentTimeoutSeconds);
  if (!/^[A-Z0-9-]{3,64}$/.test(form.code.trim().toUpperCase()) ||
    !/^[A-Z0-9-]{2,16}$/.test(form.region.trim().toUpperCase()) ||
    !/^[A-Z]{3}$/.test(form.currency.trim().toUpperCase()) || !editionIDIsValid ||
    form.salePriceMinor.trim() === '' || !Number.isSafeInteger(salePriceMinor) || salePriceMinor < 0 ||
    !Number.isSafeInteger(totalStock) || totalStock <= 0 ||
    !Number.isSafeInteger(paymentTimeoutSeconds) || paymentTimeoutSeconds < 60 || paymentTimeoutSeconds > 86400 ||
    startsAt === null || endsAt === null) {
    throw new Error('请填写有效的版本、价格、库存、时间和支付时限。');
  }
  if (startsAt.getTime() >= endsAt.getTime()) throw new Error('结束时间必须晚于开始时间。');
  return {
    code: form.code.trim().toUpperCase(),
    editionId: form.editionId.trim(),
    region: form.region.trim().toUpperCase(),
    currency: form.currency.trim().toUpperCase(),
    salePriceMinor,
    totalStock,
    startsAt: startsAt.toISOString(),
    endsAt: endsAt.toISOString(),
    paymentTimeoutSeconds
  };
}

function activityTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString('zh-CN');
}

export default function FlashSaleAdmin({ ownerId }: Props) {
  const [activities, setActivities] = useState<AdminFlashSale[]>([]);
  const [nextCursor, setNextCursor] = useState('');
  const [available, setAvailable] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState('');
  const [error, setError] = useState('');
  const [editingId, setEditingId] = useState('');
  const [form, setForm] = useState<DraftForm>(emptyDraft);
  const generation = useRef(0);
  const operation = useRef(0);
  const busyRef = useRef(false);

  useEffect(() => {
    const current = ++generation.current;
    operation.current++;
    busyRef.current = false;
    setActivities([]);
    setNextCursor('');
    setAvailable(null);
    setNotice('');
    setError('');
    setEditingId('');
    setForm(emptyDraft);
    void loadActivities('', false, current, true);
    return () => {
      generation.current++;
      operation.current++;
      busyRef.current = false;
    };
  }, [ownerId]);

  function beginOperation(force = false): number | null {
    if (busyRef.current && !force) return null;
    const current = ++operation.current;
    busyRef.current = true;
    setBusy(true);
    return current;
  }

  function ownsOperation(ownerGeneration: number, currentOperation: number): boolean {
    return generation.current === ownerGeneration && operation.current === currentOperation;
  }

  function finishOperation(ownerGeneration: number, currentOperation: number) {
    if (!ownsOperation(ownerGeneration, currentOperation)) return;
    busyRef.current = false;
    setBusy(false);
  }

  async function loadActivities(cursor = '', append = false, current = generation.current, force = false) {
    const currentOperation = beginOperation(force);
    if (currentOperation === null) return;
    setError('');
    try {
      const page = await listAdminFlashSales(cursor);
      if (!ownsOperation(current, currentOperation)) return;
      setActivities((previous) => append ? [...previous, ...page.items.filter((item) => !previous.some((existing) => existing.id === item.id))] : page.items);
      setNextCursor(page.nextCursor ?? '');
      setAvailable(true);
    } catch (loadError) {
      if (!ownsOperation(current, currentOperation)) return;
      if (hasStatus(loadError, 404)) {
        setActivities([]);
        setNextCursor('');
        setAvailable(false);
      } else {
        setAvailable(null);
        setError(message(loadError, '秒杀活动加载失败'));
      }
    } finally {
      finishOperation(current, currentOperation);
    }
  }

  function resetEditor() {
    setEditingId('');
    setForm(emptyDraft);
    setError('');
  }

  function replaceActivity(activity: AdminFlashSale) {
    setActivities((current) => {
      const found = current.some((item) => item.id === activity.id);
      return found ? current.map((item) => item.id === activity.id ? activity : item) : [activity, ...current];
    });
  }

  async function editActivity(activityId: string) {
    const current = generation.current;
    const currentOperation = beginOperation();
    if (currentOperation === null) return;
    setError('');
    setNotice('');
    try {
      const activity = await getAdminFlashSale(activityId);
      if (!ownsOperation(current, currentOperation)) return;
      if (activity.status !== 'draft') {
        setError('只有草稿活动可以编辑。');
        replaceActivity(activity);
        return;
      }
      replaceActivity(activity);
      setEditingId(activity.id);
      setForm(formFromActivity(activity));
    } catch (editError) {
      if (ownsOperation(current, currentOperation)) setError(message(editError, '秒杀草稿加载失败'));
    } finally {
      finishOperation(current, currentOperation);
    }
  }

  async function saveActivity(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    let payload: FlashSaleDraft;
    try {
      payload = payloadFromForm(form);
    } catch (validationError) {
      setError(message(validationError, '请检查活动字段'));
      setNotice('');
      return;
    }
    const current = generation.current;
    const currentOperation = beginOperation();
    if (currentOperation === null) return;
    setError('');
    setNotice('');
    try {
      const saved = editingId
        ? await updateAdminFlashSale(editingId, payload)
        : await createAdminFlashSale(payload);
      if (!ownsOperation(current, currentOperation)) return;
      replaceActivity(saved);
      setNotice(editingId ? `秒杀草稿 ${saved.code} 已保存。` : `秒杀草稿 ${saved.code} 已创建。`);
      setEditingId('');
      setForm(emptyDraft);
    } catch (saveError) {
      if (ownsOperation(current, currentOperation)) setError(message(saveError, editingId ? '秒杀草稿保存失败' : '秒杀草稿创建失败'));
    } finally {
      finishOperation(current, currentOperation);
    }
  }

  async function activate(activity: AdminFlashSale) {
    if (busyRef.current) return;
    if (!window.confirm(`激活 ${activity.code} 后商业字段将不可编辑，确认继续？`)) return;
    await changeLifecycle(activity, 'activate');
  }

  async function cancel(activity: AdminFlashSale) {
    if (busyRef.current) return;
    if (!window.confirm(`确认取消 ${activity.code}？已接受的请求不会被删除。`)) return;
    await changeLifecycle(activity, 'cancel');
  }

  async function changeLifecycle(activity: AdminFlashSale, action: 'activate' | 'cancel') {
    const current = generation.current;
    const currentOperation = beginOperation();
    if (currentOperation === null) return;
    setError('');
    setNotice('');
    try {
      const saved = action === 'activate'
        ? await activateAdminFlashSale(activity.id)
        : await cancelAdminFlashSale(activity.id);
      if (!ownsOperation(current, currentOperation)) return;
      replaceActivity(saved);
      if (editingId === saved.id) resetEditor();
      setNotice(action === 'activate' ? `秒杀活动 ${saved.code} 已激活。` : `秒杀活动 ${saved.code} 已取消。`);
    } catch (lifecycleError) {
      if (ownsOperation(current, currentOperation)) setError(message(lifecycleError, action === 'activate' ? '秒杀活动激活失败' : '秒杀活动取消失败'));
    } finally {
      finishOperation(current, currentOperation);
    }
  }

  return (
    <section className="settings-card flash-sale-admin" aria-busy={busy}>
      <div><h2>秒杀活动管理</h2><p>创建并维护活动草稿。激活后价格、版本、地区、库存和时间不可再编辑。</p></div>
      {available === false ? <p className="knowledge-disabled" role="status">当前部署未启用秒杀管理功能。</p> : available === null && busy ? <p role="status">正在检查秒杀管理功能…</p> : <>
        <form className="settings-form flash-sale-admin-form" onSubmit={saveActivity}>
          <h3>{editingId ? '编辑秒杀草稿' : '新建秒杀活动'}</h3>
          <div className="settings-grid">
            <label>活动代码<input aria-label="秒杀活动代码" required minLength={3} maxLength={64} pattern="[A-Z0-9-]+" value={form.code} onChange={(event) => setForm({ ...form, code: event.target.value.toUpperCase() })} placeholder="AUTUMN-DELUXE" /></label>
            <label>版本 ID<input aria-label="秒杀版本 ID" required inputMode="numeric" pattern="[1-9][0-9]*" value={form.editionId} onChange={(event) => setForm({ ...form, editionId: event.target.value })} placeholder="7" /></label>
            <label>地区<input aria-label="秒杀地区" required minLength={2} maxLength={16} pattern="[A-Z0-9-]+" value={form.region} onChange={(event) => setForm({ ...form, region: event.target.value.toUpperCase() })} /></label>
            <label>货币<input aria-label="秒杀货币" required minLength={3} maxLength={3} pattern="[A-Z]{3}" value={form.currency} onChange={(event) => setForm({ ...form, currency: event.target.value.toUpperCase() })} /></label>
            <label>秒杀价（分）<input aria-label="秒杀价（分）" required type="number" min="0" step="1" value={form.salePriceMinor} onChange={(event) => setForm({ ...form, salePriceMinor: event.target.value })} /></label>
            <label>总库存<input aria-label="秒杀总库存" required type="number" min="1" step="1" value={form.totalStock} onChange={(event) => setForm({ ...form, totalStock: event.target.value })} /></label>
            <label>开始时间<input aria-label="秒杀开始时间" required type="datetime-local" step="1" value={form.startsAt} onChange={(event) => setForm({ ...form, startsAt: event.target.value })} /></label>
            <label>结束时间<input aria-label="秒杀结束时间" required type="datetime-local" step="1" value={form.endsAt} onChange={(event) => setForm({ ...form, endsAt: event.target.value })} /></label>
            <label>支付时限（秒）<input aria-label="支付时限（秒）" required type="number" min="60" max="86400" step="1" value={form.paymentTimeoutSeconds} onChange={(event) => setForm({ ...form, paymentTimeoutSeconds: event.target.value })} /></label>
          </div>
          <div className="settings-actions"><button type="submit" disabled={busy}>{editingId ? '保存秒杀草稿' : '创建秒杀草稿'}</button>{editingId ? <button className="outline-button" type="button" disabled={busy} onClick={resetEditor}>取消编辑</button> : null}<button className="outline-button" type="button" disabled={busy} onClick={() => void loadActivities()}>刷新活动</button></div>
        </form>
        {notice ? <p className="success-banner" role="status">{notice}</p> : null}
        {error ? <p className="error-banner" role="alert">{error}</p> : null}
        <div className="flash-sale-admin-list">
          {activities.length === 0 ? <p>暂无秒杀活动。</p> : activities.map((activity) => <article key={activity.id}>
            <div className="flash-sale-admin-summary">
              <div><strong>{activity.code}</strong><span className={`flash-sale-admin-status ${activity.status}`}>{statusLabels[activity.status]}</span></div>
              <p>{activity.gameName || '未知游戏'} · {activity.editionName || `版本 ${activity.editionId}`}（ID: {activity.editionId}）</p>
              <p>{activity.region} / {activity.currency} · {activity.salePriceMinor} 分 · 库存 {activity.totalStock}</p>
              <small>{activityTime(activity.startsAt)} 至 {activityTime(activity.endsAt)} · 支付时限 {activity.paymentTimeoutSeconds} 秒</small>
            </div>
            <div className="flash-sale-admin-actions">
              {activity.status === 'draft' ? <><button className="outline-button" type="button" disabled={busy} onClick={() => void editActivity(activity.id)}>编辑 {activity.code}</button><button type="button" disabled={busy} onClick={() => void activate(activity)}>激活 {activity.code}</button></> : null}
              {activity.status === 'active' ? <button className="danger-button" type="button" disabled={busy} onClick={() => void cancel(activity)}>取消 {activity.code}</button> : null}
            </div>
          </article>)}
        </div>
        {nextCursor ? <button className="outline-button flash-sale-load-more" type="button" disabled={busy} onClick={() => void loadActivities(nextCursor, true)}>加载更多活动</button> : null}
      </>}
    </section>
  );
}
