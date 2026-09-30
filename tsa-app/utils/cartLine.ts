// Cart-line helpers shared by checkout and the product screen.
import type { CartItem, Product } from '@/components/services/cart';

/** The product a cart line holds, whether the API sent a bare id or a populated product. */
export function cartLineProductId(item: CartItem): string {
  if (typeof item.product === 'object' && item.product) {
    // The Go API returns `id`; `_id` is the legacy field name.
    const product = item.product as Product & { id?: string };
    return product.id || product._id;
  }
  return typeof item.product === 'string' ? item.product : '';
}

/** How many of a product the cart already holds. */
export function quantityInCart(items: CartItem[] | undefined, productId: string): number {
  return (items ?? []).reduce(
    (n, item) => (cartLineProductId(item) === productId ? n + item.quantity : n),
    0,
  );
}
