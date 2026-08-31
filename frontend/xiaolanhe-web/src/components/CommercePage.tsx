import { useEffect, useRef, useState } from 'react';
import {
  CouponClaim,
  Deal,
  Game,
  Order,
  User,
  claimCoupon,
  createOrder,
  listDeals,
  listOrders,
  payOrder
} from '../lib/api';

type Props = {
  user: User | null;
  games: Game[];
  onRequireLogin: () => void;
  onOwned: () => void;
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

export default function CommercePage({ user, games, onRequireLogin, onOwned }: Props) {
  const [tab, setTab] = useState<'deals' | 'orders'>('deals');
  const [gameId, setGameId] = useState('');
  const [deals, setDeals] = useState<Deal[]>([]);
  const [claims, setClaims] = useState<CouponClaim[]>([]);
  const [selectedClaimId, setSelectedClaimId] = useState('');
  const [orders, setOrders] = useState<Order[]>([]);
  const [busy, setBusy] = useState('');
  const [error, setError] = useState<string | null>(null);
  const claimKeys = useRef(new Map<string, string>());
  const orderKeys = useRef(new Map<string, string>());
  const paymentKeys = useRef(new Map<string, string>());
  const dealRequest = useRef(0);
  const orderRequest = useRef(0);

  useEffect(() => {
    void loadDeals();
  }, [gameId]);

  useEffect(() => {
    if (tab === 'orders' && user) void loadOrderHistory();
  }, [tab, user]);

  async function loadDeals() {
    const request = ++dealRequest.current;
    try {
      setError(null);
      const page = await listDeals(gameId);
      if (request === dealRequest.current) setDeals(page.items);
    } catch (requestError) {
      setError(message(requestError, '优惠加载失败'));
    }
  }

  async function loadOrderHistory() {
    const request = ++orderRequest.current;
    try {
      setError(null);
      const page = await listOrders();
      if (request === orderRequest.current) setOrders(page.items);
    } catch (requestError) {
      setError(message(requestError, '订单加载失败'));
    }
  }

  async function claim(deal: Deal) {
    if (!user) {
      onRequireLogin();
      return;
    }
    setBusy(`claim:${deal.code}`);
    setError(null);
    const requestKey = claimKeys.current.get(deal.code) ?? key('claim');
    claimKeys.current.set(deal.code, requestKey);
    try {
      const result = await claimCoupon(deal.code, requestKey);
      claimKeys.current.delete(deal.code);
      setClaims((current) => current.some((item) => item.id === result.claim.id) ? current : [...current, result.claim]);
      setSelectedClaimId(result.claim.id);
      await loadDeals();
    } catch (requestError) {
      setError(message(requestError, '领取失败，请重试'));
    } finally {
      setBusy('');
    }
  }

  async function checkout(game: Game, edition: NonNullable<Game['editions']>[number]) {
    if (!user) {
      onRequireLogin();
      return;
    }
    if (!edition.price) return;
    const requestIdentity = `${edition.id}:${edition.price.region}:${edition.price.currency}:${selectedClaimId}`;
    const requestKey = orderKeys.current.get(requestIdentity) ?? key('order');
    orderKeys.current.set(requestIdentity, requestKey);
    setBusy(`order:${edition.id}`);
    setError(null);
    try {
      const result = await createOrder({
        editionId: edition.id,
        region: edition.price.region,
        currency: edition.price.currency,
        ...(selectedClaimId ? { couponClaimId: selectedClaimId } : {})
      }, requestKey);
      orderKeys.current.delete(requestIdentity);
      setOrders((current) => [result.order, ...current.filter((item) => item.orderNo !== result.order.orderNo)]);
      setTab('orders');
    } catch (requestError) {
      setError(message(requestError, `无法购买 ${game.name}`));
    } finally {
      setBusy('');
    }
  }

  async function pay(order: Order) {
    orderRequest.current++;
    const requestKey = paymentKeys.current.get(order.orderNo) ?? key('payment');
    paymentKeys.current.set(order.orderNo, requestKey);
    setBusy(`payment:${order.orderNo}`);
    setError(null);
    try {
      const result = await payOrder(order.orderNo, requestKey);
      paymentKeys.current.delete(order.orderNo);
      setOrders((current) => current.map((item) => item.orderNo === result.order.orderNo ? result.order : item));
      onOwned();
    } catch (requestError) {
      setError(message(requestError, '支付确认失败，请重试'));
    } finally {
      setBusy('');
    }
  }

  return (
    <section className="page-stage commerce-stage">
      <div className="commerce-heading">
        <div><h1>优惠与游戏</h1><p>领取优惠券，按服务器价格创建订单，并使用沙箱支付解锁游戏。</p></div>
        <div className="commerce-tabs">
          <button className={tab === 'deals' ? 'active' : ''} type="button" onClick={() => setTab('deals')}>优惠与购买</button>
          <button className={tab === 'orders' ? 'active' : ''} type="button" onClick={() => user ? setTab('orders') : onRequireLogin()}>我的订单</button>
        </div>
      </div>
      {error ? <div className="error-banner commerce-error">{error}</div> : null}

      {tab === 'deals' ? <>
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
            <button type="button" disabled={busy !== '' || game.owned || !edition.price} onClick={() => void checkout(game, edition)}>
              {game.owned ? '已拥有' : busy === `order:${edition.id}` ? '创建订单中…' : '购买'}
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
