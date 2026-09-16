import type { Product } from '@/components/services/api';
import {
  CTA_LABEL,
  SERVICE_TAB_TYPE,
  listingSection,
  listingTarget,
  targetHref,
  viewAllHref,
} from '../rwmCatalogue';

function product(over: Partial<Product>): Product {
  return {
    id: 'p1',
    name: 'Thing',
    description: 'A thing',
    price: 100,
    stock: 3,
    images: [],
    status: 'active',
    type: 'Product',
    views: 0,
    sales: 0,
    rating: null,
    createdAt: '2026-07-01T00:00:00Z',
    updatedAt: '2026-07-01T00:00:00Z',
    ...over,
  } as Product;
}

describe('tab wiring', () => {
  it('maps each service tab to its category service type', () => {
    expect(SERVICE_TAB_TYPE).toEqual({ contact: 'contact', ordering: 'order', booking: 'booking' });
  });

  it('labels the CTA per tab, and gives Orders none', () => {
    expect(CTA_LABEL.products).toBe('Buy');
    expect(CTA_LABEL.contact).toBe('Contact');
    expect(CTA_LABEL.ordering).toBe('Order');
    expect(CTA_LABEL.booking).toBe('Book');
    expect(CTA_LABEL.orders).toBeNull();
  });
});

describe('listingSection', () => {
  it('prefers the embedded category title — the section tag the PM asked for', () => {
    expect(listingSection(product({ category: { id: 'c1', title: 'Flights' } }))).toBe('Flights');
  });

  it('falls back to categoryName when the category is just an id', () => {
    expect(listingSection(product({ category: 'c1', categoryName: 'Hair Extensions' }))).toBe(
      'Hair Extensions',
    );
  });

  it('falls back to location, then to nothing', () => {
    expect(listingSection(product({ location: 'Lagos' } as Partial<Product>))).toBe('Lagos');
    expect(listingSection(product({}))).toBeUndefined();
  });
});

describe('listingTarget', () => {
  it('sends a products-tab listing to the product detail screen', () => {
    const t = listingTarget(product({ id: 'abc', name: 'Photocopy machine' }), 'products');
    expect(t).toMatchObject({ kind: 'product', id: 'abc', title: 'Photocopy machine' });
  });

  it('sends a service-tab listing to the service detail screen even when it claims to be a Product', () => {
    // This is the whole point: merchants file flights under type "Product".
    const t = listingTarget(
      product({ id: 'svc', name: 'OBIC Travel agency', type: 'Product' }),
      'booking',
    );
    expect(t.kind).toBe('service');
  });

  it('carries the first image through for the detail screen header', () => {
    const t = listingTarget(
      product({ images: [{ url: 'https://x/1.png' }, { url: 'https://x/2.png' }] }),
      'contact',
    );
    expect(t.image).toBe('https://x/1.png');
  });

  it('prefers id over the legacy _id', () => {
    expect(listingTarget(product({ id: 'new', _id: 'old' }), 'products').id).toBe('new');
    expect(listingTarget(product({ id: undefined, _id: 'old' } as any), 'products').id).toBe('old');
  });
});

describe('targetHref', () => {
  it('routes a product to /product/:id', () => {
    expect(targetHref({ kind: 'product', id: 'abc', title: 'Thing' })).toBe('/product/abc');
  });

  it('routes a service to /servicedetail with the params that screen reads', () => {
    expect(
      targetHref({
        kind: 'service',
        id: 'svc',
        title: 'OBIC Travel agency',
        description: 'Flights',
        image: 'https://x/1.png',
      }),
    ).toEqual({
      pathname: '/servicedetail',
      params: {
        id: 'svc',
        title: 'OBIC Travel agency',
        description: 'Flights',
        image: 'https://x/1.png',
      },
    });
  });

  it('omits absent service params rather than sending literal undefined', () => {
    const href = targetHref({ kind: 'service', id: 'svc', title: 'Bare' }) as {
      params: Record<string, string>;
    };
    expect(href.params).toEqual({ id: 'svc', title: 'Bare' });
  });

  it('routes an order to its detail screen', () => {
    expect(targetHref({ kind: 'order', id: 'o1', title: 'Order' })).toEqual({
      pathname: '/orderdetail',
      params: { id: 'o1' },
    });
  });
});

describe('viewAllHref', () => {
  it('keeps Orders and Products on their existing destinations', () => {
    expect(viewAllHref('orders')).toBe('/orderlist');
    expect(viewAllHref('products')).toBe('/products');
  });

  it('sends the service tabs to the marketplace preselected to that sub-tab', () => {
    expect(viewAllHref('booking')).toEqual({
      pathname: '/products',
      params: { tab: 'services', serviceType: 'booking' },
    });
    expect(viewAllHref('contact')).toEqual({
      pathname: '/products',
      params: { tab: 'services', serviceType: 'contact' },
    });
    expect(viewAllHref('ordering')).toEqual({
      pathname: '/products',
      params: { tab: 'services', serviceType: 'order' },
    });
  });
});
