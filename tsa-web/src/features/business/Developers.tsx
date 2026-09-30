import { useState, type FormEvent, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { BookOpen, Clock, Lock } from 'lucide-react';
import { cn } from '@/lib/cn';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardBody, CardHeader } from '@/components/ui/card';
import { CopyBox } from '@/components/ui/copy-box';
import { Dialog } from '@/components/ui/dialog';
import { Field } from '@/components/ui/field';
import { Input } from '@/components/ui/input';
import { Note } from '@/components/ui/note';
import { toast } from '@/components/ui/toaster';
import {
  requestEndpoints, requestKeyRoll, requestWebhookSecret,
  type ActionResult, type Mode, type OwnMerchant,
} from './api';
import { BusinessPage } from './BusinessPage';
import { GuardedActionDialog } from './GuardedActionDialog';
import { SuspendedNote } from './SuspendedNote';

type Action =
  | { kind: 'key'; mode: Mode }
  | { kind: 'endpoints' }
  | { kind: 'secret'; action: 'reveal' | 'roll' };

type Shown =
  | { kind: 'key'; mode: Mode; key: string }
  | { kind: 'secret'; action: 'reveal' | 'roll'; live: string; test: string };

type Draft = { webhookUrl: string; testWebhookUrl: string; returnDomains: string; allowedIps: string };

const modeName = (m: Mode) => (m === 'live' ? 'live' : 'test');

/** One line of a card; `after` sits under it, inside the same row. */
const Row = ({ children, after }: { children: ReactNode; after?: ReactNode }) => (
  <div className="border-b border-hairline px-5 py-3.5 last:border-0">
    <div className="flex flex-wrap items-center gap-3 sm:flex-nowrap">{children}</div>
    {after && <div className="mt-3">{after}</div>}
  </div>
);

const Box = ({ children, muted }: { children: ReactNode; muted?: boolean }) => (
  <div className={cn('min-w-0 flex-1 basis-full truncate rounded-sm border border-hairline bg-background px-3 py-2 font-mono text-[12.5px] sm:basis-auto', muted && 'text-text-secondary')}>
    {children}
  </div>
);

function KeyRow({ merchant: m, mode, onRoll, locked }: { merchant: OwnMerchant; mode: Mode; onRoll: () => void; locked: boolean }) {
  const has = mode === 'live' ? m.hasLiveKey : m.hasTestKey;
  const prefix = mode === 'live' ? m.liveKeyPrefix : m.testKeyPrefix;
  const notLive = mode === 'live' && !m.liveEnabled && (
    <Note tone="info" icon={<Clock />}>Live payments are switched off — your live key is refused until TSA Connect switches them back on.</Note>
  );
  return (
    <Row after={notLive}>
      <Badge tone={mode} className="w-12 justify-center">{mode === 'live' ? 'Live' : 'Test'}</Badge>
      <Box muted={!has}>{has ? `${prefix ?? ''}••••••••••••` : 'No key yet'}</Box>
      <Button size="sm" variant={mode === 'live' && has ? 'danger' : 'secondary'} onClick={onRoll} disabled={locked}>
        {has ? 'Create new' : 'Create'} {modeName(mode)} key
      </Button>
    </Row>
  );
}

function DevelopersBody({ merchant: m }: { merchant: OwnMerchant }) {
  const [action, setAction] = useState<Action | null>(null);
  const [shown, setShown] = useState<Shown | null>(null);
  const [editing, setEditing] = useState(false);
  // One form for both cards; its title says which one opened it.
  const [editTitle, setEditTitle] = useState('Edit webhooks');
  const [draft, setDraft] = useState<Draft>({ webhookUrl: '', testWebhookUrl: '', returnDomains: '', allowedIps: '' });
  // Undefined only from an API that predates the allowlist: offering it there would save nothing.
  const allowedIps = m.allowedIps;
  // A suspended merchant's settings are read-only (the API refuses every change).
  const locked = m.status === 'suspended';

  const startEdit = (title: string) => {
    setEditTitle(title);
    setDraft({ webhookUrl: m.webhookUrl, testWebhookUrl: m.testWebhookUrl, returnDomains: m.returnDomains.join(', '), allowedIps: (allowedIps ?? []).join(', ') });
    setEditing(true);
  };
  const list = (s: string) => [...new Set(s.split(/[\s,]+/).map((d) => d.trim().toLowerCase()).filter(Boolean))];

  const guarded = (() => {
    if (!action) return null;
    if (action.kind === 'key') {
      const has = action.mode === 'live' ? m.hasLiveKey : m.hasTestKey;
      const name = modeName(action.mode);
      return {
        title: has ? `Create a new ${name} API key` : `Create your ${name} API key`,
        confirmLabel: has ? `Create new ${name} key` : `Create ${name} key`,
        danger: action.mode === 'live' && has,
        warning: has
          ? `Your current ${name} key stops working immediately when you confirm. Any server still using it will be refused until you update it with the new key.`
          : undefined,
        request: (password: string) => requestKeyRoll(action.mode, password),
      };
    }
    if (action.kind === 'endpoints') {
      return {
        title: 'Save these settings',
        confirmLabel: 'Save settings',
        request: (password: string) =>
          requestEndpoints({
            webhookUrl: draft.webhookUrl.trim(),
            testWebhookUrl: draft.testWebhookUrl.trim(),
            returnDomains: list(draft.returnDomains),
            ...(allowedIps && { allowedIps: list(draft.allowedIps) }),
            expectedVersion: m.version,
            password,
          }),
      };
    }
    const roll = action.action === 'roll';
    return {
      title: roll ? 'Create a new signing secret' : 'Reveal your signing secret',
      confirmLabel: roll ? 'Create new secret' : 'Reveal secret',
      danger: roll,
      warning: roll ? 'Your current signing secret stops working immediately when you confirm, for live and test. Webhooks your server receives will fail its signature check until you update it with the new secret.' : undefined,
      request: (password: string) => requestWebhookSecret(action.action, password),
    };
  })();

  const done = (r: ActionResult) => {
    const a = action;
    setAction(null);
    if (a?.kind === 'key' && r.key) setShown({ kind: 'key', mode: a.mode, key: r.key });
    else if (a?.kind === 'secret') setShown({ kind: 'secret', action: a.action, live: r.webhookSecretLive ?? '', test: r.webhookSecretTest ?? '' });
    else if (a?.kind === 'endpoints') toast.success('Settings saved');
  };

  const continueEdit = (e: FormEvent) => {
    e.preventDefault();
    setEditing(false);
    setAction({ kind: 'endpoints' });
  };

  return (
    <div className="grid max-w-[980px] gap-4">
      <Card>
        <CardHeader title="API keys" description="Your server creates payments with these. We only show a key once — keep it in your server's settings." />
        <KeyRow merchant={m} mode="live" locked={locked} onRoll={() => setAction({ kind: 'key', mode: 'live' })} />
        <KeyRow merchant={m} mode="test" locked={locked} onRoll={() => setAction({ kind: 'key', mode: 'test' })} />
      </Card>

      <Card>
        <CardHeader
          title="Webhooks"
          description="We POST signed events here when a payment succeeds, fails or expires."
          actions={<Button size="sm" variant="secondary" onClick={() => startEdit('Edit webhooks')} disabled={locked}>Edit webhooks</Button>}
        />
        {(['live', 'test'] as const).map((mode) => {
          const url = mode === 'live' ? m.webhookUrl : m.testWebhookUrl;
          return (
            <Row key={mode}>
              <Badge tone={mode} className="w-12 justify-center">{mode === 'live' ? 'Live' : 'Test'}</Badge>
              <Box muted={!url}>{url || 'Not set'}</Box>
            </Row>
          );
        })}
        <div className="flex flex-wrap items-center gap-3 border-t border-hairline px-5 py-4">
          <div className="min-w-[240px] flex-1">
            <p className="text-body-sm font-semibold">Signing secret</p>
            <p className="text-caption text-text-secondary">
              Verify the <code className="font-mono text-text-primary">X-TSA-Signature</code> header with it. Creating a new one replaces the secret for live and test.
            </p>
          </div>
          <Button size="sm" variant="secondary" onClick={() => setAction({ kind: 'secret', action: 'reveal' })} disabled={locked}>Reveal</Button>
          <Button size="sm" variant="danger" onClick={() => setAction({ kind: 'secret', action: 'roll' })} disabled={locked}>Create new secret</Button>
        </div>
      </Card>

      <section>
        <Card>
          <CardHeader
            title="Return domains"
            description="Where we may send your customer back after paying. Anything else is ignored."
            actions={<Button size="sm" variant="secondary" onClick={() => startEdit('Edit return domains')} aria-label="Edit return domains" disabled={locked}>Edit</Button>}
          />
          <CardBody className="flex flex-wrap gap-2">
            {m.returnDomains.length ? (
              m.returnDomains.map((d) => (
                <span key={d} className="rounded-full border border-hairline bg-surface px-3 py-[5px] text-[12.5px] font-medium">{d}</span>
              ))
            ) : (
              <p className="text-body-sm text-text-secondary">None yet.</p>
            )}
          </CardBody>
        </Card>
      </section>

      {allowedIps && (
        <section>
          <Card>
            <CardHeader
              title="Allowed IPs"
              description="Your live key only works from these addresses, so a leaked key is useless anywhere else. Your test key works from anywhere."
              actions={<Button size="sm" variant="secondary" onClick={() => startEdit('Edit allowed IPs')} aria-label="Edit allowed IPs" disabled={locked}>Edit</Button>}
            />
            <CardBody className="flex flex-wrap gap-2">
              {allowedIps.length ? (
                allowedIps.map((ip) => (
                  <span key={ip} className="rounded-full border border-hairline bg-surface px-3 py-[5px] font-mono text-[12.5px]">{ip}</span>
                ))
              ) : (
                <p className="text-body-sm text-text-secondary">Any IP address.</p>
              )}
            </CardBody>
          </Card>
        </section>
      )}

      {locked ? (
        <SuspendedNote>create a key, change your webhooks or see your signing secret</SuspendedNote>
      ) : (
        <Note tone="warning" icon={<Lock />}>
          Creating a new key, changing webhooks or allowed IPs, or revealing the secret asks for your password and a code we email you — and we tell you by email every time.
        </Note>
      )}

      <Dialog
        open={editing}
        onOpenChange={setEditing}
        title={editTitle}
        footer={
          <>
            <Button variant="secondary" onClick={() => setEditing(false)}>Cancel</Button>
            <Button type="submit" form="edit-webhooks">Continue</Button>
          </>
        }
      >
        <form id="edit-webhooks" onSubmit={continueEdit} noValidate className="grid gap-4">
          <Field label="Live webhook URL">
            <Input type="url" placeholder="https://…" value={draft.webhookUrl} onChange={(e) => setDraft({ ...draft, webhookUrl: e.target.value })} />
          </Field>
          <Field label="Test webhook URL">
            <Input type="url" placeholder="https://…" value={draft.testWebhookUrl} onChange={(e) => setDraft({ ...draft, testWebhookUrl: e.target.value })} />
          </Field>
          <Field label="Return domains" hint="Where customers may be sent back after paying, e.g. skyair.ng. Separate them with commas.">
            <Input value={draft.returnDomains} onChange={(e) => setDraft({ ...draft, returnDomains: e.target.value })} />
          </Field>
          {allowedIps && (
            <Field
              label="Allowed IPs"
              hint="Your servers' public addresses or ranges, e.g. 203.0.113.7 or 203.0.113.0/24 — list both IPv4 and IPv6 if your servers have both. Separate them with commas. Leave empty to accept your live key from anywhere."
            >
              <Input value={draft.allowedIps} onChange={(e) => setDraft({ ...draft, allowedIps: e.target.value })} />
            </Field>
          )}
        </form>
      </Dialog>

      {guarded && (
        <GuardedActionDialog
          open
          {...guarded}
          // Cancelling the webhook confirm goes back to the form, with what was typed.
          onOpenChange={(open) => {
            if (open) return;
            if (action?.kind === 'endpoints') setEditing(true);
            setAction(null);
          }}
          // Reload reads the merchant again; the webhook form comes back with what was
          // typed, and a new Continue is checked against the fresh version.
          onStale={() => {
            if (action?.kind === 'endpoints') setEditing(true);
            setAction(null);
          }}
          onDone={done}
        />
      )}

      <ResultDialog shown={shown} onClose={() => setShown(null)} />
    </div>
  );
}

/** The key or secret, once. Only Done closes it (not Escape or a stray click), and closing drops it from memory. */
function ResultDialog({ shown, onClose }: { shown: Shown | null; onClose: () => void }) {
  if (!shown) return null;
  const footer = <Button onClick={onClose}>Done</Button>;
  if (shown.kind === 'key') {
    return (
      <Dialog open dismissible={false} onOpenChange={(o) => !o && onClose()} title={`Your new ${modeName(shown.mode)} key`} footer={footer}>
        <div className="grid gap-3">
          <CopyBox label="key" value={shown.key} />
          <Note tone="gold">This is the only time you'll see this key. Copy it now.</Note>
        </div>
      </Dialog>
    );
  }
  return (
    <Dialog open dismissible={false} onOpenChange={(o) => !o && onClose()} title="Your webhook signing secret" footer={footer}>
      <div className="grid gap-4">
        <div className="grid gap-1.5">
          <p className="text-[13px] font-semibold text-text-secondary">Live</p>
          <CopyBox label="live secret" value={shown.live} />
        </div>
        <div className="grid gap-1.5">
          <p className="text-[13px] font-semibold text-text-secondary">Test</p>
          <CopyBox label="test secret" value={shown.test} />
        </div>
        <Note tone="gold">
          {shown.action === 'roll'
            ? "This is the only time you'll see these secrets. Copy them now."
            : 'Keep them in your server\'s settings. Seeing them again needs your password and a code.'}
        </Note>
      </div>
    </Dialog>
  );
}

export function Developers() {
  return (
    <BusinessPage
      title="API keys & webhooks"
      actions={
        <Button asChild variant="secondary" size="sm">
          <Link to="/docs"><BookOpen className="size-4" aria-hidden />Read the docs</Link>
        </Button>
      }
    >
      {(m) => <DevelopersBody merchant={m} />}
    </BusinessPage>
  );
}

