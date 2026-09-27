import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { Button } from './button';
import { StatusPill } from './status-pill';
import { Field } from './field';
import { Input, PasswordInput } from './input';
import { Badge } from './badge';
import { Dialog } from './dialog';
import { Drawer } from './drawer';
import { EmptyState, ErrorState } from './states';

it('a loading button is disabled and says so to screen readers', () => {
  render(<Button loading>Save</Button>);
  const b = screen.getByRole('button', { name: /save/i });
  expect(b).toBeDisabled();
  expect(b).toHaveAttribute('aria-busy', 'true');
});

it('the primary button is the gold fill with dark slate text', () => {
  render(<Button>Continue</Button>);
  expect(screen.getByRole('button', { name: 'Continue' })).toHaveClass('bg-accent', 'text-on-accent-strong', 'rounded-md');
});

it('status pills use the words merchants read', () => {
  render(<>{(['pending', 'processing', 'succeeded', 'failed', 'expired', 'cancelled'] as const).map((s) => <StatusPill key={s} status={s} />)}</>);
  for (const label of ['Waiting', 'Processing', 'Succeeded', 'Failed', 'Expired', 'Cancelled']) expect(screen.getByText(label)).toBeInTheDocument();
  // AA text on the 12% tints: the darker web-only shades, never the tint hue itself.
  expect(screen.getByText('Waiting')).toHaveClass('text-warning-strong');
  expect(screen.getByText('Processing')).toHaveClass('text-info-strong');
  expect(screen.getByText('Succeeded')).toHaveClass('text-success-strong');
  expect(screen.getByText('Failed')).toHaveClass('text-danger-strong');
  expect(screen.getByText('Cancelled')).toHaveClass('text-text-secondary');
});

it('badges render their tone', () => {
  render(<Badge tone="test">Test</Badge>);
  expect(screen.getByText('Test')).toHaveClass('bg-accent-muted');
});

it('a field labels its control and wires the error to it', () => {
  render(<Field label="Email" error="Enter your email"><Input /></Field>);
  const input = screen.getByLabelText('Email');
  expect(input).toHaveAttribute('aria-invalid', 'true');
  expect(input).toHaveAccessibleDescription('Enter your email');
});

it('a field shows a hint when there is no error', () => {
  render(<Field label="Code" hint="Six digits"><Input /></Field>);
  expect(screen.getByLabelText('Code')).toHaveAccessibleDescription('Six digits');
});

it('a password input can be revealed and hidden again', async () => {
  render(<Field label="Password"><PasswordInput /></Field>);
  const input = screen.getByLabelText('Password');
  expect(input).toHaveAttribute('type', 'password');
  const toggle = screen.getByRole('button', { name: 'Show characters' });
  expect(toggle).toHaveAttribute('aria-pressed', 'false');
  await userEvent.click(toggle);
  expect(input).toHaveAttribute('type', 'text');
  expect(toggle).toHaveAttribute('aria-pressed', 'true');
  await userEvent.click(toggle);
  expect(input).toHaveAttribute('type', 'password');
});

function DialogHarness() {
  const [open, setOpen] = useState(true);
  return (
    <Dialog open={open} onOpenChange={setOpen} title="Roll live key" description="The old key stops working." footer={<Button>Confirm</Button>}>
      <p>body</p>
    </Dialog>
  );
}

it('a dialog is titled, described and closable', async () => {
  render(<DialogHarness />);
  const dialog = screen.getByRole('dialog', { name: 'Roll live key' });
  expect(dialog).toHaveAccessibleDescription('The old key stops working.');
  await userEvent.click(screen.getByRole('button', { name: 'Close' }));
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
});

it('a drawer is a titled dialog with a close button', () => {
  render(<Drawer open onOpenChange={() => undefined} title="PNR-K8Q2ZL"><p>detail</p></Drawer>);
  expect(screen.getByRole('dialog', { name: 'PNR-K8Q2ZL' })).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Close' })).toBeInTheDocument();
});

it('states: empty shows its action, error offers a retry', async () => {
  const retry = vi.fn();
  render(<><EmptyState title="No payments yet" body="Nothing here." action={<Button>Docs</Button>} /><ErrorState title="Couldn't load" onRetry={retry} /></>);
  expect(screen.getByText('No payments yet')).toBeInTheDocument();
  expect(screen.getByRole('alert')).toHaveTextContent("Couldn't load");
  await userEvent.click(screen.getByRole('button', { name: 'Try again' }));
  expect(retry).toHaveBeenCalledTimes(1);
});
