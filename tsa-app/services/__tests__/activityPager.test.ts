/* eslint-disable import/first -- jest hoists the mocks above the imports they
   replace; writing them first documents which import each one stands in for. */

// The unified feed merges several newest-first sources into one newest-first
// list. Each "load more" must continue exactly where the last page stopped:
// nothing skipped, nothing repeated, however the sources interleave.
jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

jest.mock('../itApi', () => ({ __esModule: true, listITIntents: jest.fn() }));
jest.mock('../paycrestApi', () => ({ __esModule: true, listOrders: jest.fn() }));

import { createActivityPager } from '../activityService';

const minute = (n: number) => new Date(Date.UTC(2026, 8, 1) + n * 60_000).toISOString();

// `count` rows, newest first, at every `step` minutes starting at `offset`.
function rows(prefix: string, count: number, offset: number, step: number) {
  return Array.from({ length: count }, (_, i) => ({
    id: `${prefix}${i}`,
    createdAt: minute(offset + (count - 1 - i) * step),
  }));
}

const reply = (data: unknown) => ({ json: async () => ({ success: true, data }) });

// A fake API: paged endpoints slice by page/limit, the bills list does not page.
function serve(tables: { tp?: any[]; cashback?: any[]; bills?: any[] | Error }) {
  (global as any).fetch = jest.fn(async (url: string) => {
    const u = new URL(url);
    const page = Number(u.searchParams.get('page') ?? 1);
    const limit = Number(u.searchParams.get('limit') ?? 20);
    const slice = (all: any[] = []) => all.slice((page - 1) * limit, page * limit);
    if (u.pathname.endsWith('/users/tp-earnings')) return reply({ earnings: slice(tables.tp) });
    if (u.pathname.endsWith('/users/cashback-earnings')) return reply({ earnings: slice(tables.cashback) });
    if (u.pathname.endsWith('/bills/orders')) {
      if (tables.bills instanceof Error) throw tables.bills;
      return reply(tables.bills ?? []);
    }
    return reply([]);
  });
}

async function drain(pager: ReturnType<typeof createActivityPager>) {
  const pages: string[][] = [];
  while (pager.hasMore()) {
    const page = await pager.next();
    if (page.length === 0) break;
    pages.push(page.map((i) => i.id));
  }
  return pages;
}

const fetchedPaths = () =>
  ((global as any).fetch as jest.Mock).mock.calls.map(([url]: [string]) => new URL(url).pathname);

describe('createActivityPager', () => {
  it('interleaves paged sources newest first across pages, with no gaps or repeats', async () => {
    // TP every 2 minutes from 0, cashback every 3 minutes from 1: they interleave unevenly.
    const tp = rows('tp', 25, 0, 2);
    const cashback = rows('cb', 15, 1, 3);
    serve({ tp, cashback });

    const pager = createActivityPager(['tp', 'cashback'], 10);
    const pages = await drain(pager);

    const expected = [...tp, ...cashback]
      .sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt))
      .map((r) => r.id);
    expect(pages.map((p) => p.length)).toEqual([10, 10, 10, 10]);
    expect(pages.flat()).toEqual(expected);
    expect(pager.hasMore()).toBe(false);
  });

  it('drains an unpaged source across pages and fetches it once', async () => {
    serve({ bills: rows('bill', 30, 0, 1).map((b) => ({ ...b, category: 'airtime', status: 'vended' })) });

    const pages = await drain(createActivityPager(['bill'], 20));

    expect(pages.map((p) => p.length)).toEqual([20, 10]);
    expect(fetchedPaths().filter((p) => p.endsWith('/bills/orders'))).toHaveLength(1);
  });

  it('skips a row the server repeats when a new one shifts its offsets', async () => {
    const tp = rows('tp', 4, 0, 1);
    serve({ tp });
    const pager = createActivityPager(['tp'], 2);

    const first = await pager.next();
    // A new earning lands between pages: page 2 now starts with page 1's last row.
    tp.unshift({ id: 'tp-new', createdAt: minute(100) });
    const rest = await drain(pager);

    const all = [...first.map((i) => i.id), ...rest.flat()];
    expect(all).toEqual(['tp0', 'tp1', 'tp2', 'tp3']);
  });

  it('keeps paging the other sources when one fails', async () => {
    serve({ tp: rows('tp', 5, 0, 1), bills: new Error('network') });

    const pages = await drain(createActivityPager(['tp', 'bill'], 2));

    expect(pages.flat()).toEqual(['tp0', 'tp1', 'tp2', 'tp3', 'tp4']);
  });

  it('serves overlapping next() calls as consecutive pages', async () => {
    serve({ tp: rows('tp', 6, 0, 1) });
    const pager = createActivityPager(['tp'], 3);

    const [a, b] = await Promise.all([pager.next(), pager.next()]);

    expect(a.map((i) => i.id)).toEqual(['tp0', 'tp1', 'tp2']);
    expect(b.map((i) => i.id)).toEqual(['tp3', 'tp4', 'tp5']);
  });

  it("asks for the user's purchases only, not their sales", async () => {
    serve({});
    await createActivityPager(['order'], 20).next();

    const url = new URL(((global as any).fetch as jest.Mock).mock.calls[0][0]);
    expect(url.pathname.endsWith('/orders')).toBe(true);
    expect(url.searchParams.get('role')).toBe('buyer');
  });
});
