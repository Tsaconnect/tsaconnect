import type { FormEvent, ReactNode } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { ChevronLeft, ChevronRight, Search } from 'lucide-react';
import { useMode } from '@/components/shell/mode';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Segmented } from '@/components/ui/segmented';
import { EmptyState, ErrorState } from '@/components/ui/states';
import type { Mode, PaymentFilter, PaymentPage } from './api';
import type { PaymentColumn } from './paymentColumns';
import { PaymentsTable } from './PaymentsTable';

/**
 * A filterable, paged list of payments — the Payments page and the payment links
 * page. `kind` is both the query key and the URL segment rows open under
 * (/business/:mode/<kind>/:id).
 */
export type PaymentListProps = {
  kind: 'payments' | 'links';
  /** The word in messages: "payments", "payment links". */
  noun: string;
  statuses: readonly { value: string; label: string }[];
  load: (mode: Mode, filter: PaymentFilter, signal?: AbortSignal) => Promise<PaymentPage>;
  searchPlaceholder: string;
  /** The list before there is anything in it (no filter set). */
  empty: ReactNode;
  columns?: PaymentColumn[];
};

// Filters live in the URL, so the detail drawer, the mode switch and the back
// button all keep them.
export function PaymentList({ kind, noun, statuses, load, searchPlaceholder, empty, columns }: PaymentListProps) {
  const mode = useMode();
  const [params, setParams] = useSearchParams();
  const status = statuses.find((s) => s.value === params.get('status'))?.value ?? '';
  const q = params.get('q') ?? '';
  const page = Math.max(1, Math.floor(Number(params.get('page'))) || 1);

  const list = useQuery({
    queryKey: [kind, mode, { status, q, page }],
    queryFn: ({ signal }) => load(mode, { status, q, page }, signal),
    // Keep the rows on screen while the next page or filter loads — but never
    // across a mode switch: live rows must not show under the Test banner.
    placeholderData: (prev, prevQuery) => (prevQuery?.queryKey[1] === mode ? prev : undefined),
  });

  const update = (next: Record<string, string>) => {
    const p = new URLSearchParams(params);
    for (const [k, v] of Object.entries(next)) {
      if (v) p.set(k, v);
      else p.delete(k);
    }
    setParams(p);
  };

  const search = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    update({ q: String(new FormData(e.currentTarget).get('q') ?? '').trim(), page: '' });
  };

  const data = list.data;
  const filtered = Boolean(status || q);
  // A page past the end (a stale link, or the list shrank) has no rows but a total.
  const pastEnd = Boolean(data && data.items.length === 0 && data.total > 0);
  const first = data && data.items.length ? (data.page - 1) * data.limit + 1 : 0;
  const last = data && data.items.length ? first + data.items.length - 1 : 0;

  return (
    <Card>
      <div className="flex flex-wrap items-center gap-3 border-b border-hairline px-5 py-3">
        <Segmented
          look="chips"
          legend="Status"
          name="status"
          value={status}
          options={statuses}
          onChange={(value) => update({ status: value, page: '' })}
        />
        <form key={q} role="search" onSubmit={search} className="relative ml-auto w-full sm:w-[280px]">
          <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-control-icon" aria-hidden />
          <Input
            type="search"
            name="q"
            defaultValue={q}
            aria-label={`Search ${noun}`}
            placeholder={searchPlaceholder}
            className="h-9 pl-9"
          />
        </form>
      </div>

      {list.isError && !data ? (
        <ErrorState title={`We couldn't load your ${noun}`} body={list.error.message} onRetry={() => void list.refetch()} />
      ) : data && data.total === 0 ? (
        filtered ? (
          <EmptyState
            title={`No ${noun} match`}
            body="Try another status, or search for a different reference."
            action={<Button variant="secondary" size="sm" onClick={() => update({ status: '', q: '', page: '' })}>Clear filters</Button>}
          />
        ) : (
          empty
        )
      ) : (
        <>
          {pastEnd ? (
            <EmptyState
              title="Nothing on this page"
              body={`There are ${data!.total} ${noun}, but this page is past the end of the list.`}
              action={<Button variant="secondary" size="sm" onClick={() => update({ page: '' })}>Go to first page</Button>}
            />
          ) : (
            <PaymentsTable
              items={data?.items}
              loading={list.isPending}
              columns={columns}
              hrefFor={(p) => ({ pathname: `/business/${mode}/${kind}/${p.id}`, search: params.toString() ? `?${params}` : '' })}
            />
          )}
          {data && (
            <div className="flex flex-wrap items-center gap-2 border-t border-hairline px-5 py-3">
              <p className="flex-1 text-caption text-text-secondary tabular">
                {last ? `Showing ${first}–${last} of ${data.total}` : `Showing 0 of ${data.total}`}
              </p>
              <Button variant="secondary" size="sm" disabled={page <= 1} onClick={() => update({ page: page > 2 ? String(page - 1) : '' })}>
                <ChevronLeft className="size-4" aria-hidden />Prev
              </Button>
              <Button variant="secondary" size="sm" disabled={pastEnd || last >= data.total} onClick={() => update({ page: String(page + 1) })}>
                Next<ChevronRight className="size-4" aria-hidden />
              </Button>
            </div>
          )}
        </>
      )}
      {list.isError && data && (
        <p role="alert" className="border-t border-hairline px-5 py-2.5 text-caption text-danger-strong">
          {list.error.message}
        </p>
      )}
    </Card>
  );
}
