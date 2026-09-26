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
              Your server creates a payment, your customer pays it in the TSA Connect app, and we tell your server by webhook.
              Your API keys, webhooks and signing secret are under <b className="font-semibold text-text-primary">API keys &amp; webhooks</b> in your dashboard.
            </p>
          </div>

          <Section id="overview" lead="One base URL, one key per mode, JSON in and out.">
            <Reference
              rows={[
                ['Base URL', <C>{BASE_URL}</C>],
                ['Authentication', <>Send your key on every request: <C>Authorization: Bearer &lt;key&gt;</C>.</>],
                ['Keys', <>A live key starts <C>tsapk_live_</C>, a test key <C>tsapk_test_</C>. The key decides the mode: a test key only ever sees test payments.</>],
                ['Amounts', <>Integers in kobo: <C>8450000</C> is ₦84,500.00. The only currency is <C>NGN</C>.</>],
                ['Responses', <>Every answer is <C>{'{"success": true, "message": …, "data": …}'}</C>; a refusal is <C>{'{"success": false, "message": …, "code": …}'}</C> (see <a href="#errors" className="font-semibold text-accent-strong hover:underline">Errors</a>).</>],
                ['Limits', <>600 requests a minute per IP address, then 120 a minute per merchant.</>],
              ]}
            />
            <Note tone="warning">
              Keep your keys on your server. Anyone holding a key can create and cancel payments as you — never put one in a web page or an app.
              If one leaks, create a new one from your dashboard: the old key stops working at once.
            </Note>
          </Section>

          <Section id="create-a-payment" lead={<><C>POST /payments</C> starts one checkout for your customer. Send them to its <C>checkoutUrl</C> (or <C>appUrl</C>, which opens the TSA Connect app) to pay.</>}>
            <Reference
              head={['Field', 'Type', 'What it is']}
              rows={[
                [<C>amount</C>, 'integer', <>Required. The price in kobo, within your account's minimum and maximum — by default 100000 (₦1,000) to 500000000 (₦5,000,000).</>],
                [<C>currency</C>, 'string', <>Optional. <C>"NGN"</C>, the default and the only one accepted.</>],
                [<C>reference</C>, 'string', <>Required, 1–128 bytes once surrounding spaces are trimmed. Your own id for the order, unique per mode. Sending the same reference with the same body again returns the same payment (<C>200</C> instead of <C>201</C>), so a retry never makes a second one; the same reference with a different amount, currency, description or return URL is refused with <C>409</C>.</>],
                [<C>description</C>, 'string', 'Optional, up to 255 bytes once surrounding spaces are trimmed (a naira sign or an accented letter is more than one byte). Shown to your customer.'],
                [<C>returnUrl</C>, 'string', <>Optional. Where your customer goes back to after paying. It must be <C>https</C>, on the default port, with no user name or password, and its host must exactly match one of your return domains (a subdomain does not match). Set return domains in your dashboard.</>],
                [<C>expiresIn</C>, 'integer', <>Optional. Seconds until the payment expires unpaid: 900 (15 minutes) to 86400 (24 hours); 1800 (30 minutes) if you leave it out. It is not part of the retry comparison.</>],
              ]}
            />
            <div className="grid gap-4">
              <CodeBlock title="Create a payment with curl" code={createCurl} />
              <CodeBlock title="Create a payment in Node" code={createNode} />
            </div>
            <CodeBlock title="Response — 201 Created" code={createResponse} />
            <p className="text-body-sm text-text-secondary">Amounts are illustrative.</p>

            <H3>Read or cancel a payment</H3>
            <P>
              <C>GET /payments/:id</C> returns the payment object. <C>POST /payments/:id/cancel</C> ends a payment that is still{' '}
              <C>pending</C>: it becomes <C>expired</C> with <C>failureReason</C> <C>"cancelled by the merchant"</C>, and a{' '}
              <C>payment.expired</C> webhook is sent. A payment that is no longer pending cannot be cancelled (<C>409</C>).
            </P>
            <CodeBlock title="Read and cancel with curl" code={retrieveCurl} />
          </Section>

          <Section id="the-payment-object" lead="Create, read and cancel answer with it, and every webhook carries it as data. Optional fields are left out when empty.">
            <Reference
              head={['Field', 'Type', 'What it is']}
              rows={[
                [<C>id</C>, 'string (UUID)', 'Our id for the payment.'],
                [<C>reference</C>, 'string', 'Your reference, as you sent it.'],
                [<C>mode</C>, 'string', <><C>live</C> or <C>test</C> — the mode of the key that created it.</>],
                [<C>status</C>, 'string', <><C>pending</C>, <C>processing</C>, <C>succeeded</C>, <C>failed</C> or <C>expired</C> (below).</>],
                [<C>amount</C>, 'integer', 'Your price in kobo, as you sent it.'],
                [<C>customerTotal</C>, 'integer', 'What your customer pays, in kobo.'],
                [<C>merchantNet</C>, 'integer', 'What you are paid, in kobo.'],
                [<C>fee</C>, 'integer', <><C>customerTotal − merchantNet</C>, in kobo. Who bears the fee is set on your account. Amounts in the examples are illustrative.</>],
                [<C>currency</C>, 'string', <><C>NGN</C>.</>],
                [<C>description</C>, 'string', 'As you sent it.'],
                [<C>returnUrl</C>, 'string, optional', 'As you sent it.'],
                [<C>expiresAt</C>, 'string (RFC 3339)', 'When a still-pending payment expires.'],
                [<C>settlementRef</C>, 'string, optional', <>Our reference for the payout, once the payment succeeds. Its format varies: a payout order id, a transaction hash, or <C>"test"</C> in test mode. Store it as an opaque string.</>],
                [<C>failureReason</C>, 'string, optional', <>Why it failed, expired or was cancelled, or why an attempt was abandoned. Branch on <C>status</C>, not on this field.</>],
                [<C>succeededAt</C>, 'string (RFC 3339), optional', 'When the payment succeeded.'],
                [<C>appUrl</C>, 'string', <>Opens the payment in the TSA Connect app: <C>tsaconnect://pay?id=&lt;id&gt;</C>.</>],
                [<C>checkoutUrl</C>, 'string', 'The web page to send your customer to.'],
                [<C>createdAt</C>, 'string (RFC 3339)', 'When you created it.'],
              ]}
            />
            <H3>Statuses</H3>
            <ul className="grid max-w-[72ch] gap-2 text-body">
              <li><C>pending</C> → <C>processing</C> — a customer has started paying.</li>
              <li><C>processing</C> → <C>succeeded</C> or <C>failed</C> — the end of that attempt. Both are final.</li>
              <li><C>pending</C> → <C>expired</C> — nobody paid by <C>expiresAt</C>, you cancelled it, or your account was suspended. Final.</li>
              <li><C>processing</C> → <C>pending</C> — an attempt was abandoned and the payment can be paid again (no webhook).</li>
            </ul>
            <P>Fulfil the order on <C>succeeded</C> only, and read it from the webhook or <C>GET /payments/:id</C> — never from the customer arriving at your <C>returnUrl</C>.</P>
          </Section>

          <Section id="webhooks" lead="When a payment reaches a final status we POST an event to your webhook URL for that mode.">
            <Reference
              head={['Event', 'Sent when']}
              rows={[
                [<C>payment.succeeded</C>, 'The customer paid. Fulfil the order.'],
                [<C>payment.failed</C>, <>The attempt failed; <C>data.failureReason</C> says why.</>],
                [<C>payment.expired</C>, 'Nobody paid in time, you cancelled the payment, or your account was suspended.'],
              ]}
            />
            <P>
              Live events go to your live webhook URL and test events to your test webhook URL — test events never reach the live one, and a mode with
              no URL gets no events. A URL must be <C>https</C> on the default port and reach a public address.
            </P>
            <CodeBlock title="Event body" code={webhookBody} />
            <Reference
              head={['Header', 'Value']}
              rows={[
                [<C>Content-Type</C>, <C>application/json</C>],
                [<C>X-TSA-Signature</C>, <><C>t=&lt;timestamp&gt;,v1=&lt;signature&gt;</C> — see <a href="#verify-the-signature" className="font-semibold text-accent-strong hover:underline">Verify the signature</a>.</>],
                [<C>X-TSA-Event</C>, <>The event type, e.g. <C>payment.succeeded</C>.</>],
                [<C>X-TSA-Delivery</C>, <>The event <C>id</C>, the same as in the body.</>],
              ]}
            />
            <H3>Delivery and retries</H3>
            <ul className="grid max-w-[72ch] list-disc gap-2 pl-5 text-body">
              <li>Answer with any <C>2xx</C> within 5 seconds. Anything else — another status, a redirect (we do not follow them), a timeout — is a failed attempt.</li>
              <li>A failed delivery is tried again after 1 minute, 5 minutes, 30 minutes, 2 hours, 6 hours and 24 hours. If the last of those fails too, we stop.</li>
              <li>The same event can reach you more than once. Its <C>id</C> never changes: store it, and ignore an id you have already handled.</li>
              <li>Answer first and do slow work afterwards, so a slow job does not turn into a failed delivery and a repeat.</li>
            </ul>
          </Section>

          <Section id="verify-the-signature" lead="Check every event before you trust it: the signature proves it came from us and was not changed.">
            <P>
              Each request carries <C>X-TSA-Signature</C> in the form <C>t=&lt;timestamp&gt;,v1=&lt;signature&gt;</C>. <C>t</C> is when we signed it, in Unix
              seconds, and <C>v1</C> is the hex HMAC-SHA256 of <C>&lt;t&gt;.&lt;raw body&gt;</C> — the timestamp, a full stop, then the body exactly as sent — keyed
              with your signing secret. Each mode has its own secret (<C>whsec_…</C>); reveal them or create new ones in your dashboard.
            </P>
            <ol className="grid max-w-[72ch] list-decimal gap-2 pl-5 text-body">
              <li>Read the raw body before any JSON parsing — re-serialised JSON will not match.</li>
              <li>Recompute the HMAC with the secret for the event's mode and compare it with <C>v1</C> in constant time.</li>
              <li>Refuse an old <C>t</C> (we suggest more than 5 minutes away) so a captured request cannot be replayed. Every retry is signed afresh.</li>
            </ol>
            <CodeBlock title="Verify a webhook in Node" code={verifyNode} />
            <CodeBlock title="Use it in an Express route" code={expressNode} />
          </Section>

          <Section id="test-mode" lead="Build and test your whole integration without moving real money.">
            <ul className="grid max-w-[72ch] list-disc gap-2 pl-5 text-body">
              <li>Create a test key under API keys &amp; webhooks in your dashboard and use it exactly like a live key. Test payments have <C>"mode": "test"</C>.</li>
              <li>Set a test webhook URL there too. Test events carry <C>"livemode": false</C> and are signed with your test secret.</li>
              <li>Open a test payment's <C>appUrl</C> in the TSA Connect app, signed in as the account that owns the business, and tap <b className="font-semibold">Approve</b> or <b className="font-semibold">Decline</b>.
                Approve makes it <C>succeeded</C> (with <C>settlementRef</C> <C>"test"</C>); Decline makes it <C>failed</C> (<C>failureReason</C> <C>"declined in test mode"</C>). Either sends its webhook to your test URL.</li>
              <li>Your test payments and their webhook attempts are listed in your dashboard in test mode.</li>
              <li>
                Set up payouts first: a bank account or a crypto wallet, under{' '}
                <Link to="/business/live/payouts" className="font-semibold text-accent-strong hover:underline">Payouts</Link> in your dashboard.
                Until then every create, test or live, is refused with <C>422</C> and the message{' '}
                <C>this merchant can't take payments yet — settlement isn't configured</C> (there is no <C>code</C>).
              </li>
            </ul>
            <Note tone="info">
              Until live payments are switched on for TSA Pay, a live key is refused with <C>403 live_disabled</C>. Until we switch
              live payments on for your business — after you have made a test payment — it is refused with <C>403 live_not_enabled</C>.
              A test key always works.
            </Note>
          </Section>

          <Section id="errors" lead={<>A refusal has <C>"success": false</C>, a <C>message</C> you can log, and, where there is one, a <C>code</C> to branch on.</>}>
            <CodeBlock title="Error body" code={errorBody} />
            <Reference
              head={['Status', 'Code', 'Why']}
              rows={[
                [<C>400</C>, '—', <>The body is not valid JSON of the right shape, or the payment id is not a UUID.</>],
                [<C>401</C>, <C>invalid_key</C>, <>The <C>Authorization</C> header is missing or malformed, or the key is unknown or was replaced by a new one.</>],
                [<C>403</C>, <C>merchant_suspended</C>, 'Your merchant account is suspended.'],
                [<C>403</C>, <C>live_disabled</C>, 'A live key while live payments are not enabled.'],
                [<C>403</C>, <C>live_not_enabled</C>, "A live key before live payments are switched on for your business. Finish testing and contact TSA Connect."],
                [<C>404</C>, '—', 'Reading a payment this key cannot see: a wrong id, another merchant\'s, or one from the other mode.'],
                [<C>409</C>, '—', <>The <C>reference</C> was already used with different details, or (cancel) the payment is no longer <C>pending</C>, isn't yours, or is from the other mode.</>],
                [<C>422</C>, '—', <>A field is out of bounds (the <C>message</C> says which), or your payouts are not set up yet.</>],
                [<C>429</C>, '—', 'Too many requests. Wait and retry.'],
                [<C>500</C>, '—', 'Our fault. Retry with the same reference and body — you will not get a second payment.'],
                [<C>503</C>, <C>auth_unavailable</C>, 'We could not check your key just now. Retry shortly.'],
              ]}
            />
          </Section>
        </main>
      </div>
    </div>
  );
}
