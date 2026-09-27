import { Link } from 'react-router-dom';
import { Clock } from 'lucide-react';
import { inlineLink } from '@/components/ui/inline-link';
import { Note } from '@/components/ui/note';

/** "Switch to test mode": the payment links in test, on the reusable list when `reusable`. */
export function TestModeLink({ reusable, onClick }: { reusable?: boolean; onClick?: () => void }) {
  return (
    <Link to={`/business/test/links${reusable ? '?type=reusable' : ''}`} onClick={onClick} className={inlineLink}>
      Switch to test mode
    </Link>
  );
}

/**
 * Live payments aren't on for this business (see isLiveOff): say so before any
 * form, with the links in test mode. `doInstead` finishes the sentence.
 */
export function LiveOffNote({ doInstead, reusable, onLeave }: { doInstead: string; reusable?: boolean; onLeave?: () => void }) {
  return (
    <Note tone="info" icon={<Clock />}>
      Live payments aren't on for your business yet — {doInstead}. <TestModeLink reusable={reusable} onClick={onLeave} />
    </Note>
  );
}
