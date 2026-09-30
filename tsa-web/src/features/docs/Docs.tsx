import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { ArrowRight } from 'lucide-react';
import { DEFAULT_HOME } from '@/session/context';
import { Brand } from '@/components/shell/Brand';
import { Note } from '@/components/ui/note';
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table';
import { CodeBlock } from './CodeBlock';
import {
  BASE_URL, createCurl, createNode, createResponse, errorBody, expressNode, retrieveCurl, verifyNode, webhookBody,
} from './samples';

// The public TSA Pay developer docs. Every field, header, event, limit and error
// code below is copied from tsa-api-go — see samples.ts for the files.

const SECTIONS = [
  { id: 'overview', title: 'Overview' },
  { id: 'create-a-payment', title: 'Create a payment' },
  { id: 'the-payment-object', title: 'The payment object' },
  { id: 'webhooks', title: 'Webhooks' },
  { id: 'verify-the-signature', title: 'Verify the signature' },
  { id: 'test-mode', title: 'Test mode' },
  { id: 'errors', title: 'Errors' },
] as const;

type SectionId = (typeof SECTIONS)[number]['id'];

const C = ({ children }: { children: ReactNode }) => (
  <code className="rounded-[5px] bg-surface-alt px-1 py-px font-mono text-[0.9em] break-words text-text-primary">{children}</code>
);

const P = ({ children }: { children: ReactNode }) => <p className="max-w-[72ch] text-body text-text-primary">{children}</p>;

const H3 = ({ children }: { children: ReactNode }) => <h3 className="mt-3 text-title3 font-semibold">{children}</h3>;

function Section({ id, lead, children }: { id: SectionId; lead?: ReactNode; children: ReactNode }) {
  const title = SECTIONS.find((s) => s.id === id)!.title;
  return (
    <section id={id} aria-labelledby={`${id}-title`} className="grid min-w-0 scroll-mt-20 gap-4 border-t border-hairline pt-10">
      <div className="grid gap-2">
        <h2 id={`${id}-title`} className="text-title1 font-bold">{title}</h2>
        {lead && <p className="max-w-[72ch] text-body text-text-secondary">{lead}</p>}
      </div>
      {children}
    </section>
  );
}

/** A reference table: name, then what it is. */
function Reference({ head, rows }: { head?: string[]; rows: ReactNode[][] }) {
  return (
    <div className="min-w-0 overflow-hidden rounded-md border border-hairline bg-surface">
      <Table>
        {head && (
          <THead>
            <TR>{head.map((h) => <TH key={h} className="px-4">{h}</TH>)}</TR>
          </THead>
        )}
        <TBody>
          {rows.map((cells, i) => (
            <TR key={i} className="[tbody_&]:hover:bg-transparent">
              {cells.map((cell, j) => (
                j === 0 && !head
                  ? <TH key={j} scope="row" className="border-0 bg-transparent px-4 py-3 align-top text-body-sm normal-case text-text-primary">{cell}</TH>
                  : <TD key={j} className={j === 0 ? 'px-4 align-top whitespace-nowrap' : 'px-4 align-top text-text-primary'}>{cell}</TD>
              ))}
            </TR>
          ))}
        </TBody>
      </Table>
    </div>
  );
}

function Toc() {
  return (
    <nav aria-label="On this page" className="lg:sticky lg:top-20">
      <p className="mb-2 text-overline font-semibold uppercase tracking-[.08em] text-text-secondary">On this page</p>
      <ul className="flex flex-wrap gap-x-4 gap-y-1.5 lg:grid lg:gap-0.5">
        {SECTIONS.map((s) => (
          <li key={s.id}>
            <a href={`#${s.id}`} className="block rounded-sm py-1 text-body-sm font-medium text-text-secondary hover:text-text-primary lg:px-2 lg:hover:bg-surface-alt">
              {s.title}
            </a>
          </li>
        ))}
      </ul>
    </nav>
  );
}

export function Docs() {
  return (
    <div className="min-h-full bg-background">
      <header className="sticky top-0 z-30 bg-slate-900">
        <div className="mx-auto flex h-14 max-w-[1200px] items-center gap-3 px-4 sm:px-7">
          <Brand />
          <span className="hidden rounded-full bg-slate-elevated px-2.5 py-0.5 text-caption font-semibold text-gold-bright sm:inline">TSA Pay docs</span>
          <Link
            to={DEFAULT_HOME}
            className="ml-auto inline-flex items-center gap-1.5 rounded-sm px-2.5 py-1.5 text-body-sm font-semibold text-on-slate hover:bg-slate-elevated hover:text-white"
          >
            Your dashboard
            <ArrowRight className="size-4" aria-hidden />
          </Link>
        </div>
      </header>

      <div className="mx-auto grid max-w-[1200px] gap-8 px-4 pt-8 pb-16 sm:px-7 lg:grid-cols-[200px_minmax(0,1fr)] lg:gap-12 lg:pt-12">
        <aside className="min-w-0">
          <Toc />
        </aside>

        <main className="grid min-w-0 gap-10">
          <div className="grid gap-3">
            <p className="text-overline font-semibold uppercase tracking-[.08em] text-accent-strong">Developers</p>
            <h1 className="text-display font-bold">Take payments with TSA Pay</h1>
            <p className="max-w-[72ch] text-body text-text-secondary">
              Your server creates a payment, your customer pays in the TSA Connect app, and we notify your server by webhook.
              Keys, webhooks and your signing secret live under <b className="font-semibold text-text-primary">API keys &amp; webhooks</b> in your dashboard.
            </p>
          </div>

          <Section id="overview" lead="One base URL, one key per mode, JSON in and out.">
            <Reference
              rows={[
                ['Base URL', <C>{BASE_URL}</C>],
                ['Authentication', <><C>Authorization: Bearer &lt;key&gt;</C> on every request, over <C>https://</C> only.</>],
                ['Keys', <><C>tsapk_live_…</C> for live, <C>tsapk_test_…</C> for test. The key sets the mode.</>],
                ['Allowed IPs', <>Optional. Restrict your live key to your servers' addresses in your dashboard; anything else gets <C>403 ip_not_allowed</C>. Include IPv6 if your servers use it.</>],
                ['Amounts', <>Integers in kobo: <C>8450000</C> is ₦84,500.00. Currency is always <C>NGN</C>.</>],
                ['Responses', <><C>{'{"success": true, "message": …, "data": …}'}</C>, or on failure <C>{'{"success": false, "message": …, "code": …}'}</C>.</>],
                ['Rate limits', '600 requests a minute per IP, 120 per merchant.'],
              ]}
            />
            <Note tone="warning">
              Keep keys on your server, never in a web page or app. If a key leaks — including by being sent over <C>http://</C> — create a new one
              in your dashboard; the old one stops working immediately.
            </Note>
          </Section>

          <Section id="create-a-payment" lead={<><C>POST /payments</C> creates a checkout. Send your customer to its <C>checkoutUrl</C>, or <C>appUrl</C> to open the app.</>}>
            <Reference
              head={['Field', 'Type', 'Description']}
              rows={[
                [<C>amount</C>, 'integer', <>Required. In kobo, within your account's limits (by default ₦1,000 to ₦5,000,000).</>],
                [<C>currency</C>, 'string', <>Optional. <C>"NGN"</C> only.</>],
                [<C>reference</C>, 'string', <>Required, up to 128 bytes. Your order id, unique per mode. Resending it returns the existing payment (<C>200</C>), so retries are safe; a different amount, currency, description or return URL is refused (<C>409</C>).</>],
                [<C>description</C>, 'string', 'Optional, up to 255 bytes. Shown to your customer.'],
                [<C>returnUrl</C>, 'string', <>Optional. An <C>https</C> URL whose host exactly matches one of your return domains, set in your dashboard.</>],
                [<C>expiresIn</C>, 'integer', 'Optional. Seconds until an unpaid payment expires: 900 to 86400. Default 1800.'],
              ]}
            />
            <div className="grid gap-4">
              <CodeBlock title="Create a payment with curl" code={createCurl} />
              <CodeBlock title="Create a payment in Node" code={createNode} />
            </div>
            <CodeBlock title="Response — 201 Created" code={createResponse} />

            <H3>Read or cancel a payment</H3>
            <P>
              <C>GET /payments/:id</C> returns the payment. <C>POST /payments/:id/cancel</C> expires a <C>pending</C> payment and sends a{' '}
              <C>payment.expired</C> webhook; any other status answers <C>409</C>.
            </P>
            <CodeBlock title="Read and cancel with curl" code={retrieveCurl} />
          </Section>

          <Section id="the-payment-object" lead="Returned by every endpoint and sent as the data of every webhook. Empty optional fields are omitted.">
            <Reference
              head={['Field', 'Type', 'Description']}
              rows={[
                [<C>id</C>, 'string (UUID)', 'Our id for the payment.'],
                [<C>reference</C>, 'string', 'Your reference.'],
                [<C>mode</C>, 'string', <><C>live</C> or <C>test</C>.</>],
                [<C>status</C>, 'string', <><C>pending</C>, <C>processing</C>, <C>succeeded</C>, <C>failed</C> or <C>expired</C>.</>],
                [<C>amount</C>, 'integer', 'Your price, in kobo.'],
                [<C>customerTotal</C>, 'integer', 'What your customer pays, in kobo.'],
                [<C>merchantNet</C>, 'integer', 'What you receive, in kobo.'],
                [<C>fee</C>, 'integer', <><C>customerTotal − merchantNet</C>. Who bears it is set on your account.</>],
                [<C>currency</C>, 'string', <><C>NGN</C>.</>],
                [<C>description</C>, 'string', 'Your description.'],
                [<C>returnUrl</C>, 'string, optional', 'Your return URL.'],
                [<C>expiresAt</C>, 'string (RFC 3339)', 'When a pending payment expires.'],
                [<C>settlementRef</C>, 'string, optional', <>Our payout reference once the payment succeeds (<C>"test"</C> in test mode). Treat it as opaque.</>],
                [<C>failureReason</C>, 'string, optional', <>Why it failed or expired. For display and logs — branch on <C>status</C>.</>],
                [<C>succeededAt</C>, 'string (RFC 3339), optional', 'When it succeeded.'],
                [<C>appUrl</C>, 'string', 'Opens the payment in the TSA Connect app.'],
                [<C>checkoutUrl</C>, 'string', 'The web checkout page.'],
                [<C>createdAt</C>, 'string (RFC 3339)', 'When it was created.'],
              ]}
            />
            <H3>Statuses</H3>
            <ul className="grid max-w-[72ch] gap-2 text-body">
              <li><C>pending</C> → <C>processing</C> — the customer started paying.</li>
              <li><C>processing</C> → <C>succeeded</C> or <C>failed</C> — final.</li>
              <li><C>pending</C> → <C>expired</C> — unpaid by <C>expiresAt</C>, cancelled, or your account was suspended. Final.</li>
              <li><C>processing</C> → <C>pending</C> — the customer abandoned the attempt; it can be paid again. No webhook.</li>
            </ul>
            <P>Fulfil orders on <C>succeeded</C> only, confirmed by webhook or <C>GET /payments/:id</C> — never by the customer reaching your <C>returnUrl</C>.</P>
          </Section>

          <Section id="webhooks" lead="When a payment reaches a final status, we POST an event to that mode's webhook URL.">
            <Reference
              head={['Event', 'Sent when']}
              rows={[
                [<C>payment.succeeded</C>, 'The customer paid. Fulfil the order.'],
                [<C>payment.failed</C>, <>The payment failed; see <C>data.failureReason</C>.</>],
                [<C>payment.expired</C>, 'It expired unpaid, was cancelled, or your account was suspended.'],
              ]}
            />
            <P>
              Live and test events go to separate URLs; a mode with no URL gets no events. URLs must be <C>https</C> on a public address.
            </P>
            <CodeBlock title="Event body" code={webhookBody} />
            <Reference
              head={['Header', 'Value']}
              rows={[
                [<C>X-TSA-Signature</C>, <><C>t=&lt;timestamp&gt;,v1=&lt;signature&gt;</C> — see <a href="#verify-the-signature" className="font-semibold text-accent-strong hover:underline">Verify the signature</a>.</>],
                [<C>X-TSA-Event</C>, <>The event type, e.g. <C>payment.succeeded</C>.</>],
                [<C>X-TSA-Delivery</C>, <>The event <C>id</C>.</>],
              ]}
            />
            <H3>Delivery and retries</H3>
            <ul className="grid max-w-[72ch] list-disc gap-2 pl-5 text-body">
              <li>Reply <C>2xx</C> within 5 seconds, then do any slow work. Other statuses, redirects and timeouts count as failures.</li>
              <li>Failures are retried after 1 minute, 5 minutes, 30 minutes, 2 hours, 6 hours and 24 hours.</li>
              <li>An event can arrive more than once. Deduplicate on its <C>id</C>.</li>
            </ul>
          </Section>

          <Section id="verify-the-signature" lead="Verify every event before acting on it.">
            <P>
              <C>X-TSA-Signature</C> has the form <C>t=&lt;timestamp&gt;,v1=&lt;signature&gt;</C>: <C>t</C> is Unix seconds and <C>v1</C> is the hex
              HMAC-SHA256 of <C>&lt;t&gt;.&lt;raw body&gt;</C>, keyed with that mode's signing secret (<C>whsec_…</C>).
            </P>
            <ol className="grid max-w-[72ch] list-decimal gap-2 pl-5 text-body">
              <li>Use the raw request body — re-serialised JSON will not match.</li>
              <li>Compare your HMAC with <C>v1</C> in constant time.</li>
              <li>Reject timestamps more than 5 minutes old to prevent replays. Each retry is signed with a fresh timestamp.</li>
            </ol>
            <CodeBlock title="Verify a webhook in Node" code={verifyNode} />
            <CodeBlock title="Use it in an Express route" code={expressNode} />
          </Section>

          <Section id="test-mode" lead="Test your whole integration without moving money.">
            <ul className="grid max-w-[72ch] list-disc gap-2 pl-5 text-body">
              <li>
                Set up payouts first, under{' '}
                <Link to="/business/live/payouts" className="font-semibold text-accent-strong hover:underline">Payouts</Link>. Until then, every
                create answers <C>422</C>.
              </li>
              <li>Create a test key and a test webhook URL in your dashboard. Test events carry <C>"livemode": false</C> and use your test signing secret.</li>
              <li>
                Open a test payment's <C>appUrl</C> in the TSA Connect app as the business owner and tap <b className="font-semibold">Approve</b> or{' '}
                <b className="font-semibold">Decline</b> to make it <C>succeeded</C> or <C>failed</C>.
              </li>
              <li>Test payments and their webhook deliveries appear in your dashboard in test mode.</li>
            </ul>
          </Section>

          <Section id="errors" lead={<>Every error has a <C>message</C> to log and, where useful, a <C>code</C> to branch on.</>}>
            <CodeBlock title="Error body" code={errorBody} />
            <Reference
              head={['Status', 'Code', 'Meaning']}
              rows={[
                [<C>400</C>, '—', 'Malformed JSON, or the payment id is not a UUID.'],
                [<C>401</C>, <C>invalid_key</C>, 'Missing, malformed, unknown or replaced key.'],
                [<C>403</C>, <C>merchant_suspended</C>, 'Your account is suspended.'],
                [<C>403</C>, <C>live_disabled</C>, 'Live payments are paused on TSA Pay. Test keys still work.'],
                [<C>403</C>, <C>live_not_enabled</C>, 'Live payments are switched off for your business. Contact TSA Connect.'],
                [<C>403</C>, <C>https_required</C>, <>Sent over <C>http://</C>. Treat the key as leaked and create a new one.</>],
                [<C>403</C>, <C>ip_not_allowed</C>, <>Live key used from an address outside your Allowed IPs. The <C>message</C> names the address.</>],
                [<C>404</C>, '—', 'No such payment for this key.'],
                [<C>409</C>, '—', <>Reference reused with different details, or cancelling a payment that is not <C>pending</C> or not this key's.</>],
                [<C>422</C>, '—', <>A field is out of range (see <C>message</C>), or payouts are not set up.</>],
                [<C>429</C>, '—', 'Rate limited. Retry shortly.'],
                [<C>500</C>, '—', 'Our error. Retry with the same reference and body — no duplicate is created.'],
                [<C>503</C>, <C>auth_unavailable</C>, 'Key check temporarily unavailable. Retry shortly.'],
              ]}
            />
          </Section>
        </main>
      </div>
    </div>
  );
}
