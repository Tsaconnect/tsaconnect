import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { getNonFeaturedProducts, toggleFeatured } from '@/api/products';
import { PageHeader } from '@/components/shared/page-header';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { CheckCircle2, XCircle } from 'lucide-react';
import { toast } from 'sonner';
import { usePermission } from '@/hooks/use-permission';
import type { Product } from '@/types';

export default function AdvertRequestsPage() {
  const queryClient = useQueryClient();
  const canApprove = usePermission('products.approve');

  const { data, isLoading } = useQuery({
    queryKey: ['advert-requests'],
    queryFn: getNonFeaturedProducts,
  });

  const mutation = useMutation({
    mutationFn: (productId: string) => toggleFeatured(productId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['advert-requests'] });
      queryClient.invalidateQueries({ queryKey: ['stats'] });
      toast.success('Advert request updated');
    },
    onError: () => toast.error('Failed to update advert request'),
  });

  const products: Product[] = data?.data ?? [];

  if (isLoading) return <Skeleton className="h-64 w-full" />;

  return (
    <div>
      <PageHeader title="Advert Requests" description="Review and approve featured product requests" />

      {products.length === 0 ? (
        <p className="text-sm text-slate-500 mt-4">No pending advert requests.</p>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {products.map((product) => (
            <Card key={product.id}>
              <CardContent className="p-4">
                {product.images?.[0] && (
                  <img
                    src={product.images[0].url}
                    alt={product.name}
                    className="w-full h-40 object-cover rounded mb-3"
                  />
                )}
                <h3 className="font-medium text-slate-900">{product.name}</h3>
                <p className="text-sm text-slate-500 mt-1 line-clamp-2">{product.description}</p>
                <p className="text-sm font-medium mt-2">₦{product.price.toLocaleString()}</p>
                <p className="text-xs text-slate-400 mt-1">{product.type} · {product.stock} in stock</p>

                {canApprove && (
                  <div className="mt-3 flex gap-2">
                    <Button
                      size="sm"
                      onClick={() => mutation.mutate(product.id)}
                      disabled={mutation.isPending}
                    >
                      <CheckCircle2 className="mr-1 h-4 w-4" /> Approve
                    </Button>
                    <Button
                      size="sm"
                      variant="destructive"
                      disabled={mutation.isPending}
                    >
                      <XCircle className="mr-1 h-4 w-4" /> Reject
                    </Button>
                  </div>
                )}
              </CardContent>
            </Card>
          ))}
        </div>
      )}
    </div>
  );
}
