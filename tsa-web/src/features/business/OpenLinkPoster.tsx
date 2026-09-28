import { Link, useParams } from 'react-router-dom';
import { ChevronLeft, Pause, Printer } from 'lucide-react';
import { BrandMark } from '@/components/shell/Brand';
import { useMode } from '@/components/shell/mode';
import { Button } from '@/components/ui/button';
import { Note } from '@/components/ui/note';
import { QrCode } from '@/components/ui/qr-code';
import { Skeleton } from '@/components/ui/skeleton';
import { ErrorState } from '@/components/ui/states';
import type { Mode, OpenLink, OwnMerchant } from './api';
import { openAmountRule, openLinkTaking, shortUrl } from './display';
import { NoMerchant } from './NoMerchant';
import { printColorVars, SCAN_TO_PAY, testNotice } from './scanToPay';
import { isNoMerchant, useMerchant } from './useMerchant';
import { useOpenLink } from './useOpenLink';

// One A4 page, edge to edge: the sheet below carries its own margins, and on
// paper is a fixed 296mm (just under the page) that never splits — so there is
// never a second, blank page.
const PAGE = '@page { size: A4; margin: 0; } @media print { html, body, #root { height: auto !important; min-height: 0 !important; } }';

/** A reusable link as an A4 poster for the counter or the wall: on screen to check, then Print. */
export function OpenLinkPoster() {
  const mode = useMode();
  const id = useParams().id ?? '';
  const merchant = useMerchant();
  const detail = useOpenLink(mode, id);
  const link = detail.data?.openLink;
  const failed = merchant.error ?? detail.error;
  const taking = link && openLinkTaking(link);
  const notTaking = taking && !taking.taking ? taking.reason : undefined;

  let body;
  if (merchant.data && link) body = <Sheet merchant={merchant.data} link={link} mode={mode} />;
  else if (isNoMerchant(merchant.error)) body = <NoMerchant />;
  else if (failed) {
    body = (
      <ErrorState
        title="We couldn't load your poster"
        body={failed.message}
        onRetry={() => { void merchant.refetch(); void detail.refetch(); }}
      />
    );
  } else body = <Skeleton className="mx-auto h-[600px] w-full max-w-[210mm]" />;

  return (
    <div className="min-h-full bg-background-alt print:bg-white">
      <style>{PAGE}</style>
      <div className="flex flex-wrap items-center gap-3 border-b border-hairline bg-surface px-4 py-3 sm:px-7 print:hidden">
        <Button asChild variant="ghost" size="sm">
          <Link to={`/business/${mode}/links/open/${id}?type=reusable`}><ChevronLeft className="size-4" aria-hidden />Back to the link</Link>
        </Button>
        <h1 className="min-w-0 flex-1 truncate text-title3 font-semibold">Poster</h1>
        <Button size="sm" onClick={() => window.print()} disabled={!link}>
          <Printer className="size-4" aria-hidden />Print
        </Button>
      </div>
      {notTaking && (
        <Note tone="warning" icon={<Pause />} className="mx-4 mt-4 print:hidden sm:mx-auto sm:max-w-[210mm]">
          {notTaking}. Customers who scan this poster are told it isn't taking payments.
        </Note>
      )}
      <div className="overflow-x-auto px-4 py-6 print:overflow-visible print:p-0">{body}</div>
    </div>
  );
}

function Sheet({ merchant: m, link: l, mode }: { merchant: OwnMerchant; link: OpenLink; mode: Mode }) {
  const rule = openAmountRule(l);
  return (
    <article
      aria-label="Poster"
      style={printColorVars}
      className="mx-auto flex min-h-[297mm] w-[210mm] flex-col items-center overflow-hidden bg-white px-[18mm] py-[20mm] text-center text-[color:var(--print-ink)] shadow-[var(--shadow-lg)] [break-inside:avoid] [print-color-adjust:exact] print:h-[296mm] print:min-h-0 print:shadow-none"
    >
      <div className="flex items-center gap-3 text-[22px] font-bold tracking-[-0.01em]">
        <BrandMark className="size-11 rounded-md" />
        TSA Connect
      </div>
      <h2 className="mt-[16mm] text-[40px] leading-[1.15] font-bold break-words">{m.businessName}</h2>
      <p className="mt-3 text-[22px] break-words text-[color:var(--print-muted)]">{l.label}</p>
      <QrCode value={l.url} size={360} label="QR code for the payment link" className="mt-[12mm] w-[100mm] border-2 border-[color:var(--print-ink)] p-3" />
      <p className="mt-[10mm] text-[30px] font-bold">{SCAN_TO_PAY}</p>
      {/* The customer types the amount, inside this range. */}
      <p className="mt-3 text-[19px] text-[color:var(--print-muted)]">{rule}</p>
      <p className="mt-3 font-mono text-[17px] break-all text-[color:var(--print-muted)]">{shortUrl(l.url)}</p>
      {mode === 'test' && (
        <p className="mt-auto rounded-md border-2 border-dashed border-[color:var(--print-test)] px-5 py-2.5 text-[18px] font-semibold text-[color:var(--print-test)]">
          {testNotice('poster')}
        </p>
      )}
    </article>
  );
}
