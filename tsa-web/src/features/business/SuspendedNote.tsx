import { Lock } from 'lucide-react';
import { Note } from '@/components/ui/note';

/** Why a suspended merchant's settings are read-only; `children` says what they cannot do. */
export function SuspendedNote({ children }: { children: string }) {
  return <Note tone="warning" icon={<Lock />}>Your merchant account is suspended — contact support to {children}.</Note>;
}
