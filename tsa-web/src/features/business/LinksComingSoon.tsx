import { Link2 } from 'lucide-react';
import { Card } from '@/components/ui/card';
import { EmptyState } from '@/components/ui/states';
import { BusinessPage } from './BusinessPage';

export function LinksComingSoon() {
  return (
    <BusinessPage title="Payment links">
      {() => (
        <Card className="mx-auto max-w-2xl">
          <EmptyState
            icon={<Link2 className="size-5" aria-hidden />}
            title="Payment links are coming soon"
            body="Soon you'll be able to create a link for any amount and share it on WhatsApp — no code needed."
          />
        </Card>
      )}
    </BusinessPage>
  );
}
