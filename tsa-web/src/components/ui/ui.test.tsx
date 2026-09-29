import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState, type FormEvent } from 'react';
import { Button } from './button';
import { StatusPill } from './status-pill';
import { Field } from './field';
import { Input, PasswordInput } from './input';
import { Badge } from './badge';
import { Dialog } from './dialog';
import { Drawer } from './drawer';
import { EmptyState, ErrorState } from './states';
import { SearchSelect } from './search-select';

it('a loading button ignores clicks and says so to screen readers, but keeps the focus', async () => {
  const click = vi.fn();
  const submit = vi.fn((e: FormEvent) => e.preventDefault());
  render(<form onSubmit={submit}><Button type="submit" loading onClick={click}>Save</Button></form>);
  const b = screen.getByRole('button', { name: /save/i });
  expect(b).toHaveAttribute('aria-busy', 'true');
  expect(b).toHaveAttribute('aria-disabled', 'true');
  // Not disabled: a browser moves the focus off a focused button that becomes disabled.
  expect(b).not.toBeDisabled();
  await userEvent.click(b);
  expect(b).toHaveFocus();
  expect(click).not.toHaveBeenCalled();
  expect(submit).not.toHaveBeenCalled();
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

describe('search select', () => {
  const banks = [
    { value: '044', label: 'Access Bank' },
    { value: '058', label: 'Guaranty Trust Bank' },
    { value: '057', label: 'Zenith Bank' },
    { value: '090', label: 'Kuda Microfinance Bank' },
  ];

  function Picker({ onSubmit = vi.fn() }: { onSubmit?: () => void }) {
    const [bank, setBank] = useState('');
    return (
      <form onSubmit={(e) => { e.preventDefault(); onSubmit(); }}>
        <Field label="Bank">
          <SearchSelect value={bank} onChange={setBank} options={banks} placeholder="Choose your bank" emptyText="No bank matches that name" />
        </Field>
        <p data-testid="picked">{bank}</p>
        <button type="submit">Save</button>
      </form>
    );
  }

  it('typing narrows the list, names starting with the search first', async () => {
    render(<Picker />);
    await userEvent.type(screen.getByRole('combobox', { name: 'Bank' }), 'k');
    expect(screen.getAllByRole('option').map((o) => o.textContent)).toEqual(['Kuda Microfinance Bank', 'Access Bank', 'Guaranty Trust Bank', 'Zenith Bank']);
    await userEvent.type(screen.getByRole('combobox'), 'uda');
    expect(screen.getAllByRole('option').map((o) => o.textContent)).toEqual(['Kuda Microfinance Bank']);
  });

  it('a click picks a bank and shows its name', async () => {
    render(<Picker />);
    const box = screen.getByRole('combobox', { name: 'Bank' });
    await userEvent.type(box, 'zen');
    await userEvent.click(screen.getByRole('option', { name: 'Zenith Bank' }));
    expect(screen.getByTestId('picked')).toHaveTextContent('057');
    expect(box).toHaveValue('Zenith Bank');
    expect(box).toHaveAttribute('aria-expanded', 'false');
  });

  it('arrows and Enter pick without submitting the form', async () => {
    const submit = vi.fn();
    render(<Picker onSubmit={submit} />);
    await userEvent.type(screen.getByRole('combobox', { name: 'Bank' }), 'bank{ArrowDown}{Enter}');
    expect(screen.getByTestId('picked')).toHaveTextContent('058');
    expect(submit).not.toHaveBeenCalled();
  });

  it('tabbing in and pressing Enter submits the form, and picks nothing', async () => {
    const submit = vi.fn();
    render(<Picker onSubmit={submit} />);
    await userEvent.tab();
    expect(screen.getByRole('combobox', { name: 'Bank' })).toHaveFocus();
    await userEvent.keyboard('{Enter}');
    expect(submit).toHaveBeenCalledOnce();
    expect(screen.getByTestId('picked')).toBeEmptyDOMElement();
  });

  it('tells screen readers how many banks match', async () => {
    render(<Picker />);
    await userEvent.type(screen.getByRole('combobox', { name: 'Bank' }), 'zen');
    expect(screen.getByRole('status')).toHaveTextContent('1 result');
  });

  it('Escape closes the list without reaching a dialog around it', async () => {
    const outer = vi.fn();
    render(<div onKeyDown={(e) => e.key === 'Escape' && outer()}><Picker /></div>);
    const box = screen.getByRole('combobox', { name: 'Bank' });
    await userEvent.type(box, 'a{Escape}');
    expect(box).toHaveAttribute('aria-expanded', 'false');
    expect(outer).not.toHaveBeenCalled();
  });

  it('says so when nothing matches, and Escape keeps the earlier pick', async () => {
    render(<Picker />);
    const box = screen.getByRole('combobox', { name: 'Bank' });
    await userEvent.type(box, 'acc{Enter}');
    await userEvent.type(box, 'nothing');
    expect(screen.getByRole('status')).toHaveTextContent('No bank matches that name');
    expect(screen.queryAllByRole('option')).toHaveLength(0);
    await userEvent.keyboard('{Escape}');
    expect(box).toHaveValue('Access Bank');
    expect(screen.getByTestId('picked')).toHaveTextContent('044');
  });
});
