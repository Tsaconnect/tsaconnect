import { cartLineProductId, quantityInCart } from '../cartLine';
import type { CartItem } from '@/components/services/cart';

const line = (product: CartItem['product'], quantity: number) => ({ product, quantity }) as CartItem;

describe('cartLineProductId', () => {
  it('reads a bare product id', () => {
    expect(cartLineProductId(line('p1', 1))).toBe('p1');
  });
  it('prefers the Go API `id` on a populated product, falling back to `_id`', () => {
    expect(cartLineProductId(line({ id: 'p1', _id: 'legacy' } as any, 1))).toBe('p1');
    expect(cartLineProductId(line({ _id: 'legacy' } as any, 1))).toBe('legacy');
  });
});

describe('quantityInCart', () => {
  it('sums the lines holding the product and ignores the rest', () => {
    const items = [line('p1', 2), line('p2', 5), line({ id: 'p1' } as any, 1)];
    expect(quantityInCart(items, 'p1')).toBe(3);
  });
  it('is 0 for a product the cart does not hold, or no cart', () => {
    expect(quantityInCart([line('p2', 1)], 'p1')).toBe(0);
    expect(quantityInCart(undefined, 'p1')).toBe(0);
  });
});
