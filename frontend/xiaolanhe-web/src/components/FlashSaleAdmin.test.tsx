import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import FlashSaleAdmin from './FlashSaleAdmin';

const api = vi.hoisted(() => ({
  activateAdminFlashSale: vi.fn(),
  cancelAdminFlashSale: vi.fn(),
  createAdminFlashSale: vi.fn(),
  getAdminFlashSale: vi.fn(),
  listAdminFlashSales: vi.fn(),
  updateAdminFlashSale: vi.fn()
}));
vi.mock('../lib/api', () => api);

const draftActivity = {
  id: '41',
  code: 'AUTUMN-DELUXE',
  gameSlug: 'demo',
  gameName: 'Demo Game',
  editionId: '7',
  editionName: 'Deluxe',
  region: 'CN',
  currency: 'CNY',
  salePriceMinor: 9900,
  totalStock: 100,
  paymentTimeoutSeconds: 900,
  status: 'draft' as const,
  startsAt: '2026-09-10T12:00:00.000Z',
  endsAt: '2026-09-10T13:00:00.000Z',
  availability: 'unavailable' as const
};

beforeEach(() => {
  api.listAdminFlashSales.mockResolvedValue({ items: [draftActivity] });
  api.getAdminFlashSale.mockResolvedValue(draftActivity);
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
});

function fillDraft() {
  fireEvent.change(screen.getByLabelText('秒杀活动代码'), { target: { value: 'spring-sale' } });
  fireEvent.change(screen.getByLabelText('秒杀版本 ID'), { target: { value: '12' } });
  fireEvent.change(screen.getByLabelText('秒杀地区'), { target: { value: 'cn' } });
  fireEvent.change(screen.getByLabelText('秒杀货币'), { target: { value: 'cny' } });
  fireEvent.change(screen.getByLabelText('秒杀价（分）'), { target: { value: '8800' } });
  fireEvent.change(screen.getByLabelText('秒杀总库存'), { target: { value: '80' } });
  fireEvent.change(screen.getByLabelText('秒杀开始时间'), { target: { value: '2026-09-11T10:00:00' } });
  fireEvent.change(screen.getByLabelText('秒杀结束时间'), { target: { value: '2026-09-11T11:00:00' } });
  fireEvent.change(screen.getByLabelText('支付时限（秒）'), { target: { value: '600' } });
}

describe('FlashSaleAdmin', () => {
  it('loads drafts and creates an activity with normalized complete fields', async () => {
    api.createAdminFlashSale.mockImplementation(async (draft) => ({
      ...draftActivity, ...draft, id: '42', code: draft.code
    }));
    render(<FlashSaleAdmin ownerId="1" />);

    expect(await screen.findByText('AUTUMN-DELUXE')).toBeInTheDocument();
    expect(screen.getByText(/Demo Game · Deluxe/)).toHaveTextContent('ID: 7');
    fillDraft();
    fireEvent.click(screen.getByRole('button', { name: '创建秒杀草稿' }));

    await waitFor(() => expect(api.createAdminFlashSale).toHaveBeenCalledWith({
      code: 'SPRING-SALE',
      editionId: '12',
      region: 'CN',
      currency: 'CNY',
      salePriceMinor: 8800,
      totalStock: 80,
      startsAt: new Date('2026-09-11T10:00:00').toISOString(),
      endsAt: new Date('2026-09-11T11:00:00').toISOString(),
      paymentTimeoutSeconds: 600
    }));
    expect(await screen.findByText('秒杀草稿 SPRING-SALE 已创建。')).toBeInTheDocument();
    expect(screen.getByText('SPRING-SALE')).toBeInTheDocument();
  });

  it('allows only one create request while the first submission is pending', async () => {
    let resolveCreate: ((value: typeof draftActivity) => void) | undefined;
    api.createAdminFlashSale.mockImplementation(() => new Promise((resolve) => { resolveCreate = resolve; }));
    render(<FlashSaleAdmin ownerId="1" />);
    await screen.findByText('AUTUMN-DELUXE');
    fillDraft();

    const submit = screen.getByRole('button', { name: '创建秒杀草稿' });
    fireEvent.click(submit);
    fireEvent.click(submit);
    expect(api.createAdminFlashSale).toHaveBeenCalledOnce();

    resolveCreate?.({ ...draftActivity, id: '42', code: 'SPRING-SALE' });
    expect(await screen.findByText('秒杀草稿 SPRING-SALE 已创建。')).toBeInTheDocument();
  });

  it('loads the canonical detail before editing a draft', async () => {
    api.getAdminFlashSale.mockResolvedValue({ ...draftActivity, salePriceMinor: 9700, totalStock: 90 });
    api.updateAdminFlashSale.mockImplementation(async (_id, draft) => ({ ...draftActivity, ...draft }));
    render(<FlashSaleAdmin ownerId="1" />);

    fireEvent.click(await screen.findByRole('button', { name: '编辑 AUTUMN-DELUXE' }));
    await waitFor(() => expect(api.getAdminFlashSale).toHaveBeenCalledWith('41'));
    expect(await screen.findByRole('heading', { name: '编辑秒杀草稿' })).toBeInTheDocument();
    expect(screen.getByLabelText('秒杀价（分）')).toHaveValue(9700);
    expect(screen.getByLabelText('秒杀总库存')).toHaveValue(90);

    fireEvent.change(screen.getByLabelText('秒杀价（分）'), { target: { value: '9600' } });
    fireEvent.click(screen.getByRole('button', { name: '保存秒杀草稿' }));
    await waitFor(() => expect(api.updateAdminFlashSale).toHaveBeenCalledWith('41', expect.objectContaining({
      code: 'AUTUMN-DELUXE', editionId: '7', salePriceMinor: 9600, totalStock: 90, paymentTimeoutSeconds: 900
    })));
    expect(await screen.findByText('秒杀草稿 AUTUMN-DELUXE 已保存。')).toBeInTheDocument();
  });

  it('confirms activation and cancellation and exposes only valid actions', async () => {
    const confirm = vi.fn(() => true);
    vi.stubGlobal('confirm', confirm);
    api.activateAdminFlashSale.mockResolvedValue({ ...draftActivity, status: 'active', availability: 'upcoming' });
    api.cancelAdminFlashSale.mockResolvedValue({ ...draftActivity, status: 'cancelled', availability: 'cancelled' });
    render(<FlashSaleAdmin ownerId="1" />);

    fireEvent.click(await screen.findByRole('button', { name: '激活 AUTUMN-DELUXE' }));
    await waitFor(() => expect(api.activateAdminFlashSale).toHaveBeenCalledWith('41'));
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining('商业字段将不可编辑'));
    expect(await screen.findByText('秒杀活动 AUTUMN-DELUXE 已激活。')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '编辑 AUTUMN-DELUXE' })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '取消 AUTUMN-DELUXE' }));
    await waitFor(() => expect(api.cancelAdminFlashSale).toHaveBeenCalledWith('41'));
    expect(confirm).toHaveBeenLastCalledWith(expect.stringContaining('已接受的请求不会被删除'));
    expect(await screen.findByText('秒杀活动 AUTUMN-DELUXE 已取消。')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '取消 AUTUMN-DELUXE' })).not.toBeInTheDocument();
  });

  it('explains a missing admin route as a disabled feature', async () => {
    api.listAdminFlashSales.mockRejectedValue({ status: 404 });
    render(<FlashSaleAdmin ownerId="1" />);

    expect(await screen.findByText('当前部署未启用秒杀管理功能。')).toBeInTheDocument();
    expect(screen.queryByLabelText('秒杀活动代码')).not.toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('keeps server errors visible and does not replace them with disabled state', async () => {
    api.listAdminFlashSales.mockRejectedValue(new Error('秒杀依赖暂不可用'));
    render(<FlashSaleAdmin ownerId="1" />);

    expect(await screen.findByRole('alert')).toHaveTextContent('秒杀依赖暂不可用');
    expect(screen.queryByText('当前部署未启用秒杀管理功能。')).not.toBeInTheDocument();
  });

  it('ignores a previous owner load that resolves after the account changes', async () => {
    let resolveFirst: ((value: { items: (typeof draftActivity)[] }) => void) | undefined;
    api.listAdminFlashSales
      .mockImplementationOnce(() => new Promise((resolve) => { resolveFirst = resolve; }))
      .mockResolvedValueOnce({ items: [{ ...draftActivity, id: '52', code: 'NEW-OWNER' }] });
    const view = render(<FlashSaleAdmin ownerId="1" />);
    view.rerender(<FlashSaleAdmin ownerId="2" />);

    expect(await screen.findByText('NEW-OWNER')).toBeInTheDocument();
    resolveFirst?.({ items: [draftActivity] });
    await Promise.resolve();
    expect(screen.queryByText('AUTUMN-DELUXE')).not.toBeInTheDocument();
  });
});
