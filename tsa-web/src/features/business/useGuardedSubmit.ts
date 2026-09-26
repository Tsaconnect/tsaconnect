import { useRef, useState } from 'react';

/**
 * A password-guarded form's submit: one request at a time, a busy flag, the
 * problem to show, and the password — which is never kept past the request.
 */
export function useGuardedSubmit<Problem = string>() {
  const [password, setPassword] = useState('');
  const [problem, setProblem] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  // State lands after a render; this stops a second submit (a double Enter) before then.
  const inFlight = useRef(false);

  async function run(work: (password: string) => Promise<void>) {
    if (inFlight.current) return;
    inFlight.current = true;
    setBusy(true);
    setProblem(null);
    try {
      await work(password);
    } finally {
      setPassword('');
      inFlight.current = false;
      setBusy(false);
    }
  }

  return { password, setPassword, problem, setProblem, busy, run };
}
