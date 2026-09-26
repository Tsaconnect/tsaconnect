import { Link } from 'react-router-dom';
import { Clock } from 'lucide-react';
import { Note } from '@/components/ui/note';

/**
 * Live payments aren't on for this business (see isLiveOff): say so before any
 * form, with the same page in test mode. `doInstead` finishes the sentence.
 */
export function LiveOffNote({ page, doInstead, onLeave }: { page: string; doInstead: string; onLeave?: () => void }) {
  return (
    <Note tone="info" icon={<Clock />}>
      Live payments aren't on for your business yet — {doInstead}.{' '}
      <Link to={`/business/test/${page}`} onClick={onLeave} className="font-semibold underline underline-offset-2">Switch to test mode</Link>
    </Note>
  );
}
