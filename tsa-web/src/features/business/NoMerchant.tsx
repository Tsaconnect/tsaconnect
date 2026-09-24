import { Store } from 'lucide-react';
import { Card } from '@/components/ui/card';
import { EmptyState } from '@/components/ui/states';

export function NoMerchant() {
  return (
    <Card className="mx-auto max-w-2xl">
      <EmptyState
        icon={<Store className="size-5" aria-hidden />}
        title="No TSA Pay business yet"
        body="To accept payments, apply for TSA Pay in the TSA Connect app: Profile → Merchant application → Accept payments (TSA Pay). Once approved, your business appears here."
      />
    </Card>
  );
}
