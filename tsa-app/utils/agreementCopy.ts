// Pure copy helpers for the IT trading-agreement gate. Kept separate so the
// greeting logic is unit-testable without rendering the screen.

// agreementGreeting renders "Hello <first name>," from a full profile name,
// falling back to a plain "Hello," when the name is missing/blank. Only the
// first word is used to keep the greeting short.
export function agreementGreeting(fullName?: string | null): string {
  const first = (fullName ?? '').trim().split(/\s+/)[0];
  return first ? `Hello ${first},` : 'Hello,';
}
