import { useEffect, useState } from 'react';

/** Whole seconds remaining until `iso`, clamped at 0. */
export function secondsLeft(iso: string, now: number = Date.now()): number {
  const diffMs = new Date(iso).getTime() - now;
  return diffMs <= 0 ? 0 : Math.floor(diffMs / 1000);
}

/** Format seconds as m:ss (e.g. 65 -> "1:05"). */
export function formatCountdown(secs: number): string {
  const m = Math.floor(secs / 60);
  const s = secs % 60;
  return `${m}:${s.toString().padStart(2, '0')}`;
}

/** Live countdown to an ISO deadline. Returns { secs, label, expired }, ticking each second. */
export function useCountdown(deadlineIso?: string): { secs: number; label: string; expired: boolean } {
  const [secs, setSecs] = useState(() => (deadlineIso ? secondsLeft(deadlineIso) : 0));
  useEffect(() => {
    if (!deadlineIso) return;
    setSecs(secondsLeft(deadlineIso));
    const id = setInterval(() => setSecs(secondsLeft(deadlineIso)), 1000);
    return () => clearInterval(id);
  }, [deadlineIso]);
  return { secs, label: formatCountdown(secs), expired: secs <= 0 };
}
