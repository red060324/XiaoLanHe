import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import {
  APIError,
  CouponClaim,
  Deal,
  FlashSale,
  FlashSaleRequest,
  Game,
  Order,
  User,
  claimCoupon,
  createOrder,
  getFlashSaleRequest,
  listCouponClaims,
  listDeals,
  listFlashSales,
  listOrders,
  payOrder,
  reserveFlashSale
} from '../lib/api';

type CommerceTab = 'deals' | 'orders';

type Props = {
  user: User | null;
  games: Game[];
  onRequireLogin: () => void;
  onOwned: () => void;
  idempotencyKeys?: CommerceIdempotencyKeys;
  initialTab?: CommerceTab;
  onTabChange?: (tab: CommerceTab) => void;
};

export type CommerceIdempotencyKeys = {
  claims: Map<string, string>;
  orders: Map<string, string>;
  payments: Map<string, string>;
  flashSales: Map<string, string>;
};

export function createCommerceIdempotencyKeys(): CommerceIdempotencyKeys {
  return {
    claims: new Map(),
    orders: new Map(),
    payments: new Map(),
    flashSales: new Map()
  };
}

const FLASH_SALE_POLL_ATTEMPTS = 20;
const FLASH_SALE_POLL_INTERVAL_MS = 1000;

type RequestOwner = {
  userId: User['id'] | null;
  userGeneration: number;
  tab: CommerceTab;
  tabGeneration: number;
};

type ClaimReconciliation = {
  add?: CouponClaim;
  removeId?: CouponClaim['id'];
};

function key(prefix: string): string {
  return `${prefix}:${crypto.randomUUID()}`;
}

function money(amount: number, currency: string): string {
  return `${currency} ${(amount / 100).toFixed(2)}`;
}

function describeDeal(deal: Deal): string {
  return deal.discountType === 'percentage'
    ? `${deal.percentageBps! / 100}% 折扣`
    : `减 ${money(deal.fixedMinor!, deal.currency)}`;
}

function deleteMatchingKey(keys: Map<string, string>, identity: string, requestKey: string): void {
  if (keys.get(identity) === requestKey) keys.delete(identity);
}

function isFlashSaleTerminal(status: FlashSaleRequest['status']): boolean {
  return status === 'order_ready' || status === 'failed' || status === 'expired';
}

function waitForPoll(signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = window.setTimeout(resolve, FLASH_SALE_POLL_INTERVAL_MS);
    signal.addEventListener('abort', () => {
      window.clearTimeout(timer);
      reject(new DOMException('Aborted', 'AbortError'));
    }, { once: true });
  });
}

function flashSaleStatus(request: FlashSaleRequest): string {
  switch (request.status) {
    case 'queued': return '已排队，等待订单服务处理';
    case 'processing': return '库存已锁定，正在创建订单';
    case 'order_ready': return '抢购成功，订单已创建';
    case 'expired': return '订单已超时，库存已释放';
    case 'failed': return '抢购未完成，库存将自动释放';
  }
}

function availabilityLabel(activity: FlashSale): string {
  switch (activity.availability) {
    case 'upcoming': return '即将开始';
    case 'available': return '立即抢购';
    case 'exhausted': return '已抢完';
    case 'cancelled': return '已取消';
    case 'ended': return '已结束';
    default: return '暂不可用';
  }
}

type FlashSaleSectionProps = {
  user: User | null;
  requestKeys: Map<string, string>;
  onRequireLogin: () => void;
  onOpenOrders: () => void;
};

function FlashSaleSection({ user, requestKeys, onRequireLogin, onOpenOrders }: FlashSaleSectionProps) {
  const [activities, setActivities] = useState<FlashSale[]>([]);
  const [requests, setRequests] = useState<Record<string, FlashSaleRequest>>({});
  const [busyActivity, setBusyActivity] = useState('');
  const [error, setError] = useState<string | null>(null);
  const pollController = useRef<AbortController | null>(null);
  const ownerGeneration = useRef(0);
  const userId = user?.id ?? null;

  useEffect(() => {
    let current = true;
    void listFlashSales().then((page) => {
      if (current) setActivities(page.items);
    }).catch((requestError) => {
      if (!current) return;
      if (requestError instanceof APIError && requestError.status === 404) {
        setActivities([]);
        return;
      }
      setError(message(requestError, '限时抢购加载失败'));
    });
    return () => { current = false; };
  }, []);

  useLayoutEffect(() => {
    ownerGeneration.current++;
    pollController.current?.abort();
    pollController.current = null;
    setRequests({});
    setBusyActivity('');
    setError(null);
  }, [userId]);

  useEffect(() => () => {
    ownerGeneration.current++;
    pollController.current?.abort();
  }, []);

  async function poll(activity: FlashSale, initial: FlashSaleRequest, requestKey: string, generation: number) {
    const controller = new AbortController();
    pollController.current?.abort();
    pollController.current = controller;
    let current = initial;
    try {
      for (let attempt = 0; attempt < FLASH_SALE_POLL_ATTEMPTS && !isFlashSaleTerminal(current.status); attempt++) {
        if (attempt > 0) await waitForPoll(controller.signal);
        try {
          current = await getFlashSaleRequest(initial.requestId, controller.signal);
          if (generation !== ownerGeneration.current || controller.signal.aborted) return;
          setRequests((existing) => ({ ...existing, [activity.id]: current }));
        } catch (requestError) {
          if (controller.signal.aborted) return;
          if (attempt === FLASH_SALE_POLL_ATTEMPTS - 1) throw requestError;
        }
      }
      if (generation !== ownerGeneration.current || controller.signal.aborted) return;
      if (isFlashSaleTerminal(current.status)) {
        deleteMatchingKey(requestKeys, activity.id, requestKey);
      } else {
        setError('状态查询已暂停，可使用原请求继续查询。');
      }
    } catch (requestError) {
      if (generation === ownerGeneration.current && !controller.signal.aborted) {
        setError(message(requestError, '状态查询失败，可稍后继续查询'));
      }
    } finally {
      if (pollController.current === controller) pollController.current = null;
      if (generation === ownerGeneration.current) {
        setBusyActivity((currentBusy) => currentBusy === activity.id ? '' : currentBusy);
      }
    }
  }

  async function reserve(activity: FlashSale) {
    if (!user) {
      onRequireLogin();
      return;
    }
    const generation = ownerGeneration.current;
    const requestKey = requestKeys.get(activity.id) ?? key('flash-sale');
    requestKeys.set(activity.id, requestKey);
    setBusyActivity(activity.id);
    setError(null);
    try {
      const result = await reserveFlashSale(activity.id, requestKey);
      if (generation !== ownerGeneration.current) return;
      setRequests((existing) => ({ ...existing, [activity.id]: result.request }));
      if (isFlashSaleTerminal(result.request.status)) {
        deleteMatchingKey(requestKeys, activity.id, requestKey);
        setBusyActivity('');
        return;
      }
      await poll(activity, result.request, requestKey, generation);
    } catch (requestError) {
      if (generation === ownerGeneration.current) {
        setError(message(requestError, '抢购请求失败，请使用原请求重试'));
        setBusyActivity('');
      }
    }
  }

  if (activities.length === 0 && error === null) return null;

  return <section className="flash-sale-section" aria-labelledby="flash-sale-heading">
    <div className="flash-sale-title"><div><small>FLASH SALE</small><h2 id="flash-sale-heading">限时抢购</h2></div><p>Redis 原子锁定库存，订单异步创建。</p></div>
    {error ? <div className="error-banner" role="alert">{error}</div> : null}
    <div className="flash-sale-grid">
      {activities.map((activity) => {
        const request = requests[activity.id];
        const canReserve = activity.availability === 'available' && !isFlashSaleTerminal(request?.status ?? 'queued');
        return <article className="flash-sale-card" key={activity.id}>
          <div><small>{activity.code}</small><h3>{activity.gameName}</h3><p>{activity.editionName} · {money(activity.salePriceMinor, activity.currency)}</p></div>
          <p className={`flash-sale-availability ${activity.availability}`}>{availabilityLabel(activity)}</p>
          {request ? <p className={`flash-sale-request ${request.status}`} aria-live="polite">{flashSaleStatus(request)}</p> : null}
          {request?.status === 'order_ready' ? <button type="button" onClick={onOpenOrders}>查看订单</button> : <button
            type="button"
            disabled={busyActivity !== '' || !canReserve}
            onClick={() => void reserve(activity)}
          >{busyActivity === activity.id ? '处理中…' : request && !isFlashSaleTerminal(request.status) ? '继续查询' : availabilityLabel(activity)}</button>}
        </article>;
      })}
    </div>
  </section>;
}

export default function CommercePage({
  user,
  games,
  onRequireLogin,
  onOwned,
  idempotencyKeys,
  initialTab = 'deals',
  onTabChange
}: Props) {
  const [tab, setTab] = useState<CommerceTab>(initialTab);
  const [gameId, setGameId] = useState('');
  const [deals, setDeals] = useState<Deal[]>([]);
  const [claims, setClaims] = useState<CouponClaim[]>([]);
  const [selectedClaimId, setSelectedClaimId] = useState('');
  const [orders, setOrders] = useState<Order[]>([]);
  const [busy, setBusy] = useState('');
  const [error, setError] = useState<string | null>(null);
  const localIdempotencyKeys = useRef<CommerceIdempotencyKeys | null>(null);
  if (localIdempotencyKeys.current === null) {
    localIdempotencyKeys.current = createCommerceIdempotencyKeys();
  }
  const operationKeys = idempotencyKeys ?? localIdempotencyKeys.current;
  const dealRequest = useRef(0);
  const claimRequest = useRef(0);
  const orderRequest = useRef(0);
  const claimsLoadedFor = useRef<User['id'] | null>(null);
  const userOwner = useRef({ id: user?.id ?? null, generation: 0 });
  const tabOwner = useRef({ tab, generation: 0 });
  const initialTabOwner = useRef(initialTab);
  const userId = user?.id ?? null;

  useLayoutEffect(() => {
    const userChanged = userOwner.current.id !== userId;
    if (userChanged) {
      userOwner.current = { id: userId, generation: userOwner.current.generation + 1 };
      operationKeys.claims.clear();
      operationKeys.orders.clear();
      operationKeys.payments.clear();
      operationKeys.flashSales.clear();
    }
    dealRequest.current++;
    claimRequest.current++;
    orderRequest.current++;
    claimsLoadedFor.current = null;
    setDeals([]);
    setClaims([]);
    setSelectedClaimId('');
    setOrders([]);
    setBusy('');
    setError(null);
  }, [userId]);

  useLayoutEffect(() => () => {
    userOwner.current = {
      id: userOwner.current.id,
      generation: userOwner.current.generation + 1
    };
    tabOwner.current = {
      tab: tabOwner.current.tab,
      generation: tabOwner.current.generation + 1
    };
    dealRequest.current++;
    claimRequest.current++;
    orderRequest.current++;
    claimsLoadedFor.current = null;
  }, []);

  useLayoutEffect(() => {
    if (initialTabOwner.current === initialTab) return;
    initialTabOwner.current = initialTab;
    changeTab(initialTab, false);
  }, [initialTab]);

  useEffect(() => {
    if (tab === 'deals') void loadDeals(requestOwner('deals'));
  }, [gameId, tab, userId]);

  useEffect(() => {
    if (tab === 'orders' && userId !== null) void loadOrderHistory(requestOwner('orders'));
  }, [tab, userId]);

  useEffect(() => {
    if (tab !== 'deals' || userId === null || claimsLoadedFor.current === userId) return;
    const request = ++claimRequest.current;
    void loadClaims(request, requestOwner('deals'));
  }, [tab, userId]);

  function requestOwner(expectedTab: CommerceTab): RequestOwner {
    return {
      userId: userOwner.current.id,
      userGeneration: userOwner.current.generation,
      tab: expectedTab,
      tabGeneration: tabOwner.current.generation
    };
  }

  function ownsRequest(owner: RequestOwner): boolean {
    return ownsUserRequest(owner)
      && owner.tab === tabOwner.current.tab
      && owner.tabGeneration === tabOwner.current.generation;
  }

  function ownsUserRequest(owner: RequestOwner): boolean {
    return owner.userId === userOwner.current.id
      && owner.userGeneration === userOwner.current.generation;
  }

  function changeTab(nextTab: CommerceTab, notify = true) {
    if (tabOwner.current.tab === nextTab) return;
    tabOwner.current = { tab: nextTab, generation: tabOwner.current.generation + 1 };
    dealRequest.current++;
    claimRequest.current++;
    orderRequest.current++;
    setBusy('');
    setError(null);
    setTab(nextTab);
    if (notify) onTabChange?.(nextTab);
  }

  async function loadDeals(owner: RequestOwner) {
    if (!ownsRequest(owner)) return;
    const request = ++dealRequest.current;
    try {
      setError(null);
      const page = await listDeals(gameId);
      if (ownsRequest(owner) && request === dealRequest.current) setDeals(page.items);
    } catch (requestError) {
      if (ownsRequest(owner) && request === dealRequest.current) setError(message(requestError, '优惠加载失败'));
    }
  }

  async function loadOrderHistory(owner: RequestOwner) {
    if (!ownsRequest(owner)) return;
    const request = ++orderRequest.current;
    try {
      setError(null);
      const page = await listOrders();
      if (ownsRequest(owner) && request === orderRequest.current) setOrders(page.items);
    } catch (requestError) {
      if (ownsRequest(owner) && request === orderRequest.current) setError(message(requestError, '订单加载失败'));
    }
  }

  async function loadClaims(request: number, owner: RequestOwner, reconciliation?: ClaimReconciliation) {
    try {
      const page = await listCouponClaims();
      if (!ownsRequest(owner) || request !== claimRequest.current) return;
      claimsLoadedFor.current = owner.userId;
      if (reconciliation) {
        setClaims(() => {
          const merged = new Map(page.items.map((claim) => [claim.id, claim]));
          if (reconciliation.add) merged.set(reconciliation.add.id, reconciliation.add);
          if (reconciliation.removeId) merged.delete(reconciliation.removeId);
          return [...merged.values()];
        });
        if (reconciliation.removeId) {
          setSelectedClaimId((current) => current === reconciliation.removeId ? '' : current);
        }
      } else {
        setClaims(page.items);
        setSelectedClaimId((current) => page.items.some((claim) => claim.id === current) ? current : '');
      }
    } catch (requestError) {
      if (!reconciliation && ownsRequest(owner) && request === claimRequest.current) {
        setError(message(requestError, '优惠券加载失败'));
      }
    }
  }

  function refreshClaims(owner: RequestOwner, reconciliation: ClaimReconciliation) {
    claimsLoadedFor.current = null;
    const request = ++claimRequest.current;
    void loadClaims(request, owner, reconciliation);
  }

  async function claim(deal: Deal) {
    if (!user) {
      onRequireLogin();
      return;
    }
    const owner = requestOwner('deals');
    const selectionAtStart = selectedClaimId;
    const busyKey = `claim:${deal.code}`;
    setBusy(busyKey);
    setError(null);
    const requestKey = operationKeys.claims.get(deal.code) ?? key('claim');
    operationKeys.claims.set(deal.code, requestKey);
    try {
      const result = await claimCoupon(deal.code, requestKey);
      if (!ownsUserRequest(owner)) return;
      deleteMatchingKey(operationKeys.claims, deal.code, requestKey);
      if (!ownsRequest(owner)) {
        claimsLoadedFor.current = null;
        return;
      }
      setClaims((current) => current.some((item) => item.id === result.claim.id) ? current : [...current, result.claim]);
      setSelectedClaimId((current) => current === selectionAtStart ? result.claim.id : current);
      setDeals((current) => current.map((item) => item.code === deal.code && item.viewerClaimCount < item.perUserLimit
        ? { ...item, remainingStock: Math.max(0, item.remainingStock - 1), viewerClaimCount: item.viewerClaimCount + 1 }
        : item));
      refreshClaims(owner, { add: result.claim });
    } catch (requestError) {
      if (ownsRequest(owner)) setError(message(requestError, '领取失败，请重试'));
    } finally {
      if (ownsRequest(owner)) setBusy((current) => current === busyKey ? '' : current);
    }
  }

  async function checkout(game: Game, edition: NonNullable<Game['editions']>[number]) {
    if (!user) {
      onRequireLogin();
      return;
    }
    if (!edition.price) return;
    const owner = requestOwner('deals');
    const claimId = selectedClaimId;
    const busyKey = `order:${edition.id}`;
    const requestIdentity = `${edition.id}:${edition.price.region}:${edition.price.currency}:${claimId}`;
    const requestKey = operationKeys.orders.get(requestIdentity) ?? key('order');
    operationKeys.orders.set(requestIdentity, requestKey);
    setBusy(busyKey);
    setError(null);
    try {
      const result = await createOrder({
        editionId: edition.id,
        region: edition.price.region,
        currency: edition.price.currency,
        ...(claimId ? { couponClaimId: claimId } : {})
      }, requestKey);
      if (!ownsRequest(owner)) return;
      deleteMatchingKey(operationKeys.orders, requestIdentity, requestKey);
      if (claimId) {
        setClaims((current) => current.filter((claim) => claim.id !== claimId));
        setSelectedClaimId((current) => current === claimId ? '' : current);
      }
      setOrders((current) => [result.order, ...current.filter((item) => item.orderNo !== result.order.orderNo)]);
      changeTab('orders');
      if (claimId) refreshClaims(requestOwner('orders'), { removeId: claimId });
    } catch (requestError) {
      if (ownsRequest(owner)) setError(message(requestError, `无法购买 ${game.name}`));
    } finally {
      if (ownsRequest(owner)) setBusy((current) => current === busyKey ? '' : current);
    }
  }

  async function pay(order: Order) {
    const owner = requestOwner('orders');
    orderRequest.current++;
    const busyKey = `payment:${order.orderNo}`;
    const requestKey = operationKeys.payments.get(order.orderNo) ?? key('payment');
    operationKeys.payments.set(order.orderNo, requestKey);
    setBusy(busyKey);
    setError(null);
    try {
      const result = await payOrder(order.orderNo, requestKey);
      if (!ownsRequest(owner)) return;
      deleteMatchingKey(operationKeys.payments, order.orderNo, requestKey);
      setOrders((current) => current.map((item) => item.orderNo === result.order.orderNo ? result.order : item));
      onOwned();
    } catch (requestError) {
      if (ownsRequest(owner)) setError(message(requestError, '支付确认失败，请重试'));
    } finally {
      if (ownsRequest(owner)) setBusy((current) => current === busyKey ? '' : current);
    }
  }

  return (
    <section className="commerce-stage">
      <div className="commerce-heading">
        <div><h1>优惠与游戏</h1><p>领取优惠券，按服务器价格创建订单，并使用沙箱支付解锁游戏。</p></div>
        <div className="commerce-tabs">
          <button className={tab === 'deals' ? 'active' : ''} type="button" aria-pressed={tab === 'deals'} onClick={() => changeTab('deals')}>优惠与购买</button>
          <button className={tab === 'orders' ? 'active' : ''} type="button" aria-pressed={tab === 'orders'} onClick={() => user ? changeTab('orders') : onRequireLogin()}>我的订单</button>
        </div>
      </div>
      {error ? <div className="error-banner commerce-error" role="alert">{error}</div> : null}

      {tab === 'deals' ? <>
        <FlashSaleSection
          user={user}
          requestKeys={operationKeys.flashSales}
          onRequireLogin={onRequireLogin}
          onOpenOrders={() => changeTab('orders')}
        />
        <label className="deal-filter">适用游戏
          <select value={gameId} onChange={(event) => setGameId(event.target.value)}>
            <option value="">全部优惠</option>
            {games.map((game) => <option key={game.id} value={game.id}>{game.name}</option>)}
          </select>
        </label>
        <div className="deal-grid">
          {deals.map((deal) => <article className="deal-card" key={deal.id}>
            <small>{deal.code}</small><h2>{deal.name}</h2><strong>{describeDeal(deal)}</strong>
            <p>满 {money(deal.minimumMinor, deal.currency)} 可用 · 剩余 {deal.remainingStock}</p>
            <button type="button" disabled={busy !== '' || deal.remainingStock === 0 || deal.viewerClaimCount >= deal.perUserLimit} onClick={() => void claim(deal)}>
              {deal.viewerClaimCount >= deal.perUserLimit ? '已领取' : busy === `claim:${deal.code}` ? '领取中…' : '领取优惠券'}
            </button>
          </article>)}
          {deals.length === 0 ? <p className="empty-state compact">当前没有可领取优惠。</p> : null}
        </div>
        <label className="claim-selector">本次结算优惠券
          <select value={selectedClaimId} onChange={(event) => setSelectedClaimId(event.target.value)}>
            <option value="">不使用优惠券</option>
            {claims.map((claim) => <option key={claim.id} value={claim.id}>{claim.couponCode} · #{claim.id}</option>)}
          </select>
        </label>
        <div className="purchase-list">
          {games.flatMap((game) => (game.editions ?? []).map((edition) => <article key={edition.id}>
            <div><h2>{game.name}</h2><p>{edition.name} · {edition.price ? money(edition.price.amountMinor, edition.price.currency) : '暂未定价'}</p></div>
            <button type="button" disabled={busy !== '' || edition.owned || !edition.price} onClick={() => void checkout(game, edition)}>
              {edition.owned ? '已拥有' : busy === `order:${edition.id}` ? '创建订单中…' : '购买'}
            </button>
          </article>))}
        </div>
      </> : !user ? <p className="empty-state">登录后查看订单。</p> : <div className="order-list">
        {orders.map((order) => <article className="order-card" key={order.orderNo}>
          <div><small>{order.orderNo}</small><h2>{order.item.gameName} · {order.item.editionName}</h2><p>{money(order.totalMinor, order.currency)}{order.discountMinor > 0 ? `（已优惠 ${money(order.discountMinor, order.currency)}）` : ''}</p></div>
          <div className={`order-status ${order.status}`}>{order.status === 'paid' ? '已拥有' : order.status === 'pending_payment' ? '待支付' : order.status}</div>
          {order.status === 'pending_payment' ? <button type="button" disabled={busy !== ''} onClick={() => void pay(order)}>{busy === `payment:${order.orderNo}` ? '确认中…' : '沙箱支付'}</button> : null}
        </article>)}
        {orders.length === 0 ? <p className="empty-state">还没有订单。</p> : null}
      </div>}
    </section>
  );
}

function message(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback;
}
