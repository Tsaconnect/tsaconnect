import { SellerWithProducts, Subcategory } from '@/types/marketplace';

// Generate realistic dummy data
export const DUMMY_SUBCATEGORIES: Subcategory[] = [
  { id: 'electronics', name: 'Electronics', icon: '📱', description: 'Gadgets and electronic devices' },
  { id: 'fashion', name: 'Fashion', icon: '👗', description: 'Clothing and accessories' },
  { id: 'home', name: 'Home & Garden', icon: '🏠', description: 'Home improvement and garden supplies' },
  { id: 'sports', name: 'Sports', icon: '⚽', description: 'Sports equipment and apparel' },
  { id: 'books', name: 'Books', icon: '📚', description: 'Books and educational materials' },
  { id: 'beauty', name: 'Beauty', icon: '💄', description: 'Beauty and personal care products' },
  { id: 'toys', name: 'Toys & Games', icon: '🎮', description: 'Toys and entertainment products' },
  { id: 'automotive', name: 'Automotive', icon: '🚗', description: 'Car parts and accessories' },
];

export const generateDummySellers = (subcategoryId: string): SellerWithProducts[] => {
  const subcategory = DUMMY_SUBCATEGORIES.find(sc => sc.id === subcategoryId);
  
  // Base seller templates
  const sellerTemplates = [
    {
      name: 'Premium Electronics Store',
      email: 'contact@premiumelectronics.com',
      profileImage: 'https://images.unsplash.com/photo-1563013544-824ae1b704d3',
      rating: 4.8,
      location: 'San Francisco, CA',
      description: 'Premium electronics and gadgets with warranty',
      joinedDate: '2022-01-15',
      totalSales: 1245,
    },
    {
      name: 'Tech Gadgets Hub',
      email: 'sales@techgadgets.com',
      profileImage: 'https://images.unsplash.com/photo-1556157382-2eda3e8a9944',
      rating: 4.5,
      location: 'New York, NY',
      description: 'Latest tech gadgets and accessories',
      joinedDate: '2021-11-30',
      totalSales: 892,
    },
    {
      name: 'Budget Electronics',
      email: 'info@budgetelectronics.com',
      profileImage: 'https://images.unsplash.com/photo-1567532939604-b6b5b0db2604',
      rating: 4.2,
      location: 'Austin, TX',
      description: 'Affordable electronics for everyone',
      joinedDate: '2023-03-22',
      totalSales: 567,
    },
    {
      name: 'Fashion Boutique Elite',
      email: 'hello@fashionboutique.com',
      profileImage: 'https://images.unsplash.com/photo-1494790108755-2616b612b786',
      rating: 4.7,
      location: 'Los Angeles, CA',
      description: 'Trendy fashion and accessories',
      joinedDate: '2020-08-14',
      totalSales: 2310,
    },
    {
      name: 'Home Essentials Plus',
      email: 'support@homeessentials.com',
      profileImage: 'https://images.unsplash.com/photo-1560448204-e02f11c3d0e2',
      rating: 4.6,
      location: 'Chicago, IL',
      description: 'Quality home and garden products',
      joinedDate: '2021-05-10',
      totalSales: 1789,
    },
  ];

  // Product templates based on subcategory
  const getProductTemplates = (sellerIndex: number) => {
    const baseProducts: any[] = [];
    
    switch (subcategoryId) {
      case 'electronics':
        baseProducts.push(
          { name: 'Wireless Headphones', price: 129.99, image: 'https://images.unsplash.com/photo-1505740420928-5e560c06d30e' },
          { name: 'Smart Watch Pro', price: 299.99, image: 'https://images.unsplash.com/photo-1523275335684-37898b6baf30' },
          { name: 'Bluetooth Speaker', price: 89.99, image: 'https://images.unsplash.com/photo-1546435770-a3e426bf472b' },
          { name: 'Gaming Keyboard', price: 149.99, image: 'https://images.unsplash.com/photo-1541140532154-b024d705b90a' },
          { name: 'USB-C Hub', price: 49.99, image: 'https://images.unsplash.com/photo-1589003077984-894e133dabab' },
          { name: 'Phone Case', price: 29.99, image: 'https://images.unsplash.com/photo-1563013544-824ae1b704d3' },
        );
        break;
      case 'fashion':
        baseProducts.push(
          { name: 'Summer Dress', price: 59.99, image: 'https://images.unsplash.com/photo-1561047029-3000c68339ca' },
          { name: 'Leather Jacket', price: 199.99, image: 'https://images.unsplash.com/photo-1551028719-00167b16eac5' },
          { name: 'Running Shoes', price: 129.99, image: 'https://images.unsplash.com/photo-1549298916-b41d501d3772' },
          { name: 'Designer Handbag', price: 299.99, image: 'https://images.unsplash.com/photo-1584917865442-de89df76afd3' },
          { name: 'Sunglasses', price: 89.99, image: 'https://images.unsplash.com/photo-1572635196237-14b3f281503f' },
        );
        break;
      case 'home':
        baseProducts.push(
          { name: 'Ceramic Vase', price: 39.99, image: 'https://images.unsplash.com/photo-1556228453-efd6c1ff04f6' },
          { name: 'Desk Lamp', price: 49.99, image: 'https://images.unsplash.com/photo-1507473885765-e6ed057f782c' },
          { name: 'Throw Pillow', price: 29.99, image: 'https://images.unsplash.com/photo-1586023492125-27b2c045efd7' },
          { name: 'Gardening Tools', price: 79.99, image: 'https://images.unsplash.com/photo-1513530534585-c7b1394c6d51' },
        );
        break;
      default:
        baseProducts.push(
          { name: `${subcategory?.name} Product 1`, price: 99.99, image: 'https://images.unsplash.com/photo-1505740420928-5e560c06d30e' },
          { name: `${subcategory?.name} Product 2`, price: 149.99, image: 'https://images.unsplash.com/photo-1523275335684-37898b6baf30' },
          { name: `${subcategory?.name} Product 3`, price: 79.99, image: 'https://images.unsplash.com/photo-1546868871-7041f2a55e12' },
        );
    }
    
    return baseProducts;
  };

  return sellerTemplates.slice(0, 3 + (subcategoryId.charCodeAt(0) % 3)).map((template, index) => {
    const productTemplates = getProductTemplates(index);
    const productsCount = 3 + (index % 4); // Vary product count per seller
    
    const products = productTemplates.slice(0, productsCount).map((product, productIndex) => ({
      productId: `prod_${subcategoryId}_${index}_${productIndex}`,
      name: `${product.name} ${index + 1}`,
      price: product.price * (0.8 + (index * 0.1)), // Vary prices
      image: product.image + `?ixlib=rb-1.2.1&auto=format&fit=crop&w=300&q=${index + productIndex}`,
      subcategory: subcategoryId,
      sellerId: `seller_${subcategoryId}_${index}`,
      description: `High-quality ${product.name.toLowerCase()} from ${template.name}`,
      category: subcategory?.name,
      stock: 10 + (index * 5) + productIndex,
      tags: ['featured', 'new', subcategoryId],
      createdAt: `2023-${(index + 1).toString().padStart(2, '0')}-${(productIndex + 1).toString().padStart(2, '0')}`,
    }));

    return {
      sellerId: `seller_${subcategoryId}_${index}`,
      ...template,
      productCount: products.length,
      products,
    };
  });
};

// Search function with dummy data
export const searchDummyData = (
  subcategoryId: string,
  query: string
): SellerWithProducts[] => {
  const allSellers = generateDummySellers(subcategoryId);
  
  if (!query.trim()) return allSellers;

  const normalizedQuery = query.toLowerCase().trim();
  
  return allSellers
    .map(seller => {
      const filteredProducts = seller.products.filter(product =>
        product.name.toLowerCase().includes(normalizedQuery) ||
        product.productId.toLowerCase().includes(normalizedQuery) ||
        product.description?.toLowerCase().includes(normalizedQuery) ||
        product.tags?.some(tag => tag.toLowerCase().includes(normalizedQuery))
      );
      
      const sellerMatches = 
        seller.sellerId.toLowerCase().includes(normalizedQuery) ||
        seller.email.toLowerCase().includes(normalizedQuery) ||
        seller.name.toLowerCase().includes(normalizedQuery) ||
        seller.location?.toLowerCase().includes(normalizedQuery) ||
        seller.description?.toLowerCase().includes(normalizedQuery);
      
      return {
        ...seller,
        products: sellerMatches ? seller.products : filteredProducts
      };
    })
    .filter(seller => 
      seller.products.length > 0 ||
      seller.sellerId.toLowerCase().includes(normalizedQuery) ||
      seller.email.toLowerCase().includes(normalizedQuery) ||
      seller.name.toLowerCase().includes(normalizedQuery) ||
      seller.location?.toLowerCase().includes(normalizedQuery)
    );
};