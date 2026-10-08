import { useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Download } from 'lucide-react';
import { downloadFile } from '@/lib/download';
import { naira, usd } from '@/lib/format';
import { useMode } from '@/components/shell/mode';
import { Button } from '@/components/ui/button';
import { Card, CardBody, CardHeader } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { ErrorState } from '@/components/ui/states';
import { StatusPill, type PayStatus } from '@/components/ui/status-pill';
import { toast } from '@/components/ui/toaster';
import { EXPORT_MAX_DAYS, EXPORT_MAX_ROWS, getMerchant, getPaymentSummary, paymentsExportUrl, type Mode } from './api';
import { BusinessPage } from './BusinessPage';
import { DateRangeFields, type DateRange } from './DateRangeFields';
import { monthToDate, periodProblem } from './period';
import { queryKeys } from './queryKeys';
import { RefundsList } from './RefundsList';

// How a period's payments are listed, by how they ended.
const STATUS_ORDER: PayStatus[] = ['succeeded', 'processing', 'pending', 'failed', 'expired', 'cancelled'];

export function Reconciliation() {
  return <BusinessPage title="Refunds & reconciliation">{() => <ReconciliationBody />}</BusinessPage>;
}

function ReconciliationBody() {
  const mode = useMode();
  const [params, setParams] = useSearchParams();
  // This month so far, fixed when the page opens. A period she picks lives in the URL.
  const [defaults] = useState(() => monthToDate());
  const from = params.get('from') ?? defaults.from;
  const to = params.get('to') ?? defaults.to;
  const problem = periodProblem(from, to) ?? (from && to ? null : 'Choose both a start and an end date.');

  // Kept even when blank: a cleared date asks for one rather than snapping back to this month.
  const setPeriod = (next: DateRange) => setParams(new URLSearchParams({ ...Object.fromEntries(params), ...next }), { replace: true });

  return (
    <div className="grid gap-4">
      <Card>
        <CardHeader
          title="Period"
          description="Days in West Africa Time. The totals and the export count each payment on the day it was paid."
        />
        <CardBody>
          <DateRangeFields value={{ from, to }} onChange={setPeriod} legend="Paid between" problem={problem} />
        </CardBody>
      </Card>
      {!problem && (
        <>
          <PeriodTotals mode={mode} from={from} to={to} />
          <RefundsList mode={mode} from={from} to={to} />
        </>
      )}
    </div>
  );
}

function PeriodTotals({ mode, from, to }: { mode: Mode; from: string; to: string }) {
  const summary = useQuery({
    queryKey: queryKeys.summary(mode, from, to),
    queryFn: ({ signal }) => getPaymentSummary(mode, from, to, signal),
  });
  if (summary.isError) {
    return (
      <Card>
        <ErrorState title="We couldn't load this period" body={summary.error.message} onRetry={() => void summary.refetch()} />
      </Card>
    );
  }
  const s = summary.data;
  return (
    <>
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <Tile label="Payments paid" value={s && String(s.succeeded.count)} />
        <Tile label="Customers paid" value={s && naira(s.succeeded.customerTotal)} />
        <Tile label="You received" value={s && naira(s.succeeded.merchantNet)} />
        <Tile label="Refunded" value={s && usd(s.refunds.amount)} hint={s && `${s.refunds.count} ${s.refunds.count === 1 ? 'refund' : 'refunds'}`} />
      </div>
      <ExportCard mode={mode} from={from} to={to} paid={s?.succeeded.count} />
      <StatusBreakdown byStatus={s?.byStatus} />
    </>
  );
}

function Tile({ label, value, hint }: { label: string; value?: string; hint?: string }) {
  return (
    <Card className="grid gap-1 p-4">
      <p className="text-caption font-semibold text-text-secondary">{label}</p>
      {value === undefined ? <Skeleton className="h-7 w-28" /> : <p className="text-title2 font-bold tabular">{value}</p>}
      {hint && <p className="text-caption text-text-secondary">{hint}</p>}
    </Card>
  );
}

function ExportCard({ mode, from, to, paid }: { mode: Mode; from: string; to: string; paid?: number }) {
  const [preparing, setPreparing] = useState(false);
  const why =
    periodProblem(from, to, EXPORT_MAX_DAYS) ??
    (paid === 0 ? 'Nothing was paid in this period.'
      : paid !== undefined && paid > EXPORT_MAX_ROWS ? 'More than 10,000 payments were paid in this period. Choose a shorter period.'
      : null);

  const exportCsv = async () => {
    setPreparing(true);
    try {
      // The download is a plain link, outside api()'s refresh, so renew an idle session first.
      await getMerchant();
      await downloadFile(paymentsExportUrl(mode, from, to), `tsa-pay-${mode}-${from}-${to}.csv`);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Something went wrong. Please try again.');
    } finally {
      setPreparing(false);
    }
  };

  return (
    <Card>
      <CardHeader
        title="Export"
        description="Every payment paid in this period, one row each, as a CSV for your spreadsheet or accounting software."
        actions={
          <Button size="sm" variant="secondary" disabled={paid === undefined || Boolean(why)} loading={preparing} onClick={() => void exportCsv()}>
            <Download className="size-4" aria-hidden />Export CSV
          </Button>
        }
      />
      {why && <CardBody><p className="text-body-sm text-text-secondary">{why}</p></CardBody>}
    </Card>
  );
}

function StatusBreakdown({ byStatus }: { byStatus?: Record<string, number> }) {
  const shown = STATUS_ORDER.filter((s) => (byStatus?.[s] ?? 0) > 0);
  return (
    <Card>
      <CardHeader title="Payments made in this period" description="By how they ended, counted on the day each was made." />
      <CardBody>
        {byStatus === undefined ? (
          <Skeleton className="h-5 w-64" />
        ) : shown.length === 0 ? (
          <p className="text-body-sm text-text-secondary">No payments were made in this period.</p>
        ) : (
          <ul className="flex flex-wrap gap-x-6 gap-y-2">
            {shown.map((s) => (
              <li key={s} className="flex items-center gap-2">
                <StatusPill status={s} />
                <b className="tabular">{byStatus[s]}</b>
              </li>
            ))}
          </ul>
        )}
      </CardBody>
    </Card>
  );
}
