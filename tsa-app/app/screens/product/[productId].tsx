// app/screens/product/[productId].tsx
import React, { useState, useCallback, useEffect } from 'react';
import {
    ScrollView,
    View,
    Text,
    StyleSheet,
    Image,
    TouchableOpacity,
    StatusBar,
    Dimensions,
    Alert,
} from 'react-native';
import { useLocalSearchParams, useRouter, Stack } from 'expo-router';
import { Ionicons } from '@expo/vector-icons';
import Icon from 'react-native-vector-icons/MaterialIcons';
import api, { Product } from '@/components/services/api';
import { SafeAreaView } from "react-native-safe-area-context"

const { width } = Dimensions.get('window');

export default function ProductDetailScreen() {
    const { productId, productData } = useLocalSearchParams<{
        productId: string;
        productData?: string;
    }>();

    const router = useRouter();
    const [product, setProduct] = useState<Product | null>(null);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState<string | null>(null);
    const [selectedImage, setSelectedImage] = useState(0);
    const [quantity, setQuantity] = useState(1);
    const [isInCart, setIsInCart] = useState(false);

    // Load product data
    useEffect(() => {
        const loadProduct = async () => {
            try {
                setLoading(true);

                // First try to use the passed product data
                if (productData) {
                    try {
                        const parsedProduct = JSON.parse(productData);
                        setProduct(parsedProduct);
                    } catch (e) {
                        console.log('Could not parse productData');
                    }
                }

                // Then fetch fresh data from API
                if (productId) {
                    const response = await api.getProductById(productId);
                    if (response.success && response.data) {
                        setProduct(response.data);
                    } else {
                        throw new Error(response.message || 'Failed to load product');
                    }
                }
            } catch (err: any) {
                setError(err.message || 'Failed to load product details');
            } finally {
                setLoading(false);
            }
        };

        loadProduct();
    }, [productId, productData]);

    // Check if product is in cart
    useEffect(() => {
        // You would implement actual cart checking logic here
        // For now, we'll simulate it
        const checkCartStatus = async () => {
            // Simulate API call to check if product is in cart
            setIsInCart(false);
        };

        if (product) {
            checkCartStatus();
        }
    }, [product]);

    const handleBackPress = useCallback(() => {
        if (router.canGoBack()) {
            router.back();
        } else {
            router.push('/marketplace');
        }
    }, [router]);

    const handleAddToCart = useCallback(() => {
        if (!product) return;

        // Here you would implement actual cart logic
        Alert.alert(
            'Added to Cart',
            `Added ${quantity} x ${product.name} to your cart`,
            [
                { text: 'Continue Shopping', style: 'cancel' },
                { text: 'View Cart', onPress: () => router.push('/cart') },
            ]
        );

        setIsInCart(true);
    }, [product, quantity, router]);

    const handleBuyNow = useCallback(() => {
        if (!product) return;

        // Navigate to checkout
        router.push({
            pathname: '/checkout',
            params: {
                productId: product._id,
                quantity: quantity.toString(),
            }
        });
    }, [product, quantity, router]);

    const handleShare = useCallback(() => {
        if (!product) return;

        // Implement share functionality
        Alert.alert(
            'Share Product',
            'Share this product with friends',
            [
                { text: 'Cancel', style: 'cancel' },
                {
                    text: 'Share', onPress: () => {
                        // Actual share implementation would go here
                    }
                },
            ]
        );
    }, [product]);

    const handleWishlist = useCallback(() => {
        if (!product) return;

        // Implement wishlist functionality
        Alert.alert(
            'Added to Wishlist',
            `${product.name} has been added to your wishlist`,
            [{ text: 'OK' }]
        );
    }, [product]);

    const handleContactSeller = useCallback(() => {
        if (!product) return;

        // Navigate to contact or message seller
        router.push({
            pathname: '/contact',
            params: {
                sellerId: product.userId,
                sellerEmail: product.email,
                sellerName: product.companyName || 'Seller',
                productId: product._id,
            }
        });
    }, [product, router]);

    const incrementQuantity = useCallback(() => {
        if (product && quantity < product.stock) {
            setQuantity(prev => prev + 1);
        }
    }, [product, quantity]);

    const decrementQuantity = useCallback(() => {
        if (quantity > 1) {
            setQuantity(prev => prev - 1);
        }
    }, [quantity]);

    if (loading) {
        return (
            <SafeAreaView style={styles.safeArea}>
                <Stack.Screen
                    options={{
                        title: 'Loading...',
                        headerShown: true,
                        headerBackTitle: 'Back',
                        headerTintColor: '#D4AF37',
                    }}
                />
                <View style={styles.centered}>
                    <Text>Loading product...</Text>
                </View>
            </SafeAreaView>
        );
    }

    if (error || !product) {
        return (
            <SafeAreaView style={styles.safeArea}>
                <Stack.Screen
                    options={{
                        title: 'Error',
                        headerShown: true,
                    }}
                />
                <View style={styles.centered}>
                    <Text style={styles.errorText}>{error || 'Product not found'}</Text>
                    <TouchableOpacity
                        style={styles.retryButton}
                        onPress={() => router.back()}
                    >
                        <Text style={styles.retryButtonText}>Go Back</Text>
                    </TouchableOpacity>
                </View>
            </SafeAreaView>
        );
    }

    const images = product.images || [];
    const primaryImage = images[selectedImage]?.url;
    const price = typeof product.price === 'number' ? product.price.toFixed(2) : '0.00';
    const totalPrice = (product.price * quantity).toFixed(2);
    const rating = product.rating?.average || 0;
    const ratingCount = product.rating?.count || 0;
    const isInStock = product.stock > 0;

    return (
        <SafeAreaView style={styles.safeArea}>
            <StatusBar barStyle="dark-content" backgroundColor="#FFFFFF" />

            <Stack.Screen
                options={{
                    title: product.name.substring(0, 20) + (product.name.length > 20 ? '...' : ''),
                    headerShown: true,
                    headerBackTitle: 'Back',
                    headerTintColor: '#D4AF37',
                    headerRight: () => (
                        <View style={styles.headerButtons}>
                            <TouchableOpacity
                                onPress={handleShare}
                                style={styles.headerButton}
                            >
                                <Ionicons name="share-outline" size={22} color="#D4AF37" />
                            </TouchableOpacity>
                            <TouchableOpacity
                                onPress={handleWishlist}
                                style={styles.headerButton}
                            >
                                <Ionicons name="heart-outline" size={22} color="#D4AF37" />
                            </TouchableOpacity>
                        </View>
                    ),
                }}
            />

            <ScrollView style={styles.scrollView} showsVerticalScrollIndicator={false}>
                {/* Product Images */}
                <View style={styles.imageContainer}>
                    {primaryImage ? (
                        <Image
                            source={{ uri: primaryImage }}
                            style={styles.mainImage}
                            resizeMode="cover"
                        />
                    ) : (
                        <View style={[styles.mainImage, styles.placeholderImage]}>
                            <Icon name="image" size={60} color="#999" />
                        </View>
                    )}

                    {/* Image Gallery */}
                    {images.length > 1 && (
                        <ScrollView
                            horizontal
                            showsHorizontalScrollIndicator={false}
                            style={styles.imageGallery}
                        >
                            {images.map((img, index) => (
                                <TouchableOpacity
                                    key={index}
                                    style={[
                                        styles.thumbnail,
                                        selectedImage === index && styles.selectedThumbnail,
                                    ]}
                                    onPress={() => setSelectedImage(index)}
                                >
                                    <Image
                                        source={{ uri: img.url }}
                                        style={styles.thumbnailImage}
                                        resizeMode="cover"
                                    />
                                </TouchableOpacity>
                            ))}
                        </ScrollView>
                    )}
                </View>

                {/* Product Info */}
                <View style={styles.infoContainer}>
                    {/* Stock Status */}
                    <View style={styles.stockContainer}>
                        {isInStock ? (
                            <View style={styles.inStockBadge}>
                                <Ionicons name="checkmark-circle" size={16} color="#28A745" />
                                <Text style={styles.inStockText}>In Stock ({product.stock} available)</Text>
                            </View>
                        ) : (
                            <View style={styles.outOfStockBadge}>
                                <Ionicons name="close-circle" size={16} color="#DC3545" />
                                <Text style={styles.outOfStockText}>Out of Stock</Text>
                            </View>
                        )}

                        {product.isFeatured && (
                            <View style={styles.featuredBadge}>
                                <Ionicons name="star" size={14} color="#FFFFFF" />
                                <Text style={styles.featuredText}>Featured</Text>
                            </View>
                        )}
                    </View>

                    {/* Product Name */}
                    <Text style={styles.productName}>{product.name}</Text>

                    {/* Price */}
                    <View style={styles.priceContainer}>
                        <Text style={styles.price}>${price}</Text>
                        {product.type === 'Product' && product.stock > 0 && (
                            <Text style={styles.unitPrice}>
                                ${(product.price / quantity).toFixed(2)} per item
                            </Text>
                        )}
                    </View>

                    {/* Rating */}
                    {rating > 0 && (
                        <View style={styles.ratingContainer}>
                            <View style={styles.stars}>
                                {[1, 2, 3, 4, 5].map((star) => (
                                    <Ionicons
                                        key={star}
                                        name={star <= rating ? 'star' : 'star-outline'}
                                        size={20}
                                        color="#FFD700"
                                    />
                                ))}
                            </View>
                            <Text style={styles.ratingText}>
                                {rating.toFixed(1)} ({ratingCount} reviews)
                            </Text>
                        </View>
                    )}

                    {/* Description */}
                    <View style={styles.section}>
                        <Text style={styles.sectionTitle}>Description</Text>
                        <Text style={styles.description}>{product.description}</Text>
                    </View>

                    {/* Attributes */}
                    {product.attributes && product.attributes.length > 0 && (
                        <View style={styles.section}>
                            <Text style={styles.sectionTitle}>Specifications</Text>
                            <View style={styles.attributesContainer}>
                                {product.attributes.map((attr, index) => (
                                    <View key={index} style={styles.attributeRow}>
                                        <Text style={styles.attributeName}>{attr.name}:</Text>
                                        <Text style={styles.attributeValue}>{attr.value}</Text>
                                    </View>
                                ))}
                            </View>
                        </View>
                    )}

                    {/* Seller Info */}
                    <View style={styles.section}>
                        <Text style={styles.sectionTitle}>Seller Information</Text>
                        <View style={styles.sellerInfo}>
                            <View style={styles.sellerAvatar}>
                                <Icon name="store" size={24} color="#D4AF37" />
                            </View>
                            <View style={styles.sellerDetails}>
                                <Text style={styles.sellerName}>
                                    {product.companyName || 'Independent Seller'}
                                </Text>
                                <Text style={styles.sellerLocation}>
                                    <Ionicons name="location-outline" size={12} color="#666" />
                                    {' '}{product.location}
                                </Text>
                                <Text style={styles.sellerContact}>
                                    <Ionicons name="mail-outline" size={12} color="#666" />
                                    {' '}{product.email}
                                </Text>
                            </View>
                            <TouchableOpacity
                                style={styles.contactButton}
                                onPress={handleContactSeller}
                            >
                                <Text style={styles.contactButtonText}>Contact</Text>
                            </TouchableOpacity>
                        </View>
                    </View>

                    {/* Quantity Selector */}
                    {isInStock && (
                        <View style={styles.quantitySection}>
                            <Text style={styles.sectionTitle}>Quantity</Text>
                            <View style={styles.quantityContainer}>
                                <TouchableOpacity
                                    style={styles.quantityButton}
                                    onPress={decrementQuantity}
                                    disabled={quantity <= 1}
                                >
                                    <Ionicons name="remove" size={20} color={quantity <= 1 ? '#999' : '#1A1A1A'} />
                                </TouchableOpacity>
                                <Text style={styles.quantityText}>{quantity}</Text>
                                <TouchableOpacity
                                    style={styles.quantityButton}
                                    onPress={incrementQuantity}
                                    disabled={quantity >= product.stock}
                                >
                                    <Ionicons name="add" size={20} color={quantity >= product.stock ? '#999' : '#1A1A1A'} />
                                </TouchableOpacity>
                                <Text style={styles.quantityStock}>
                                    {product.stock} available
                                </Text>
                            </View>
                        </View>
                    )}
                </View>
            </ScrollView>

            {/* Add to Cart/Checkout Footer */}
            {isInStock && (
                <View style={styles.footer}>
                    <View style={styles.footerInfo}>
                        <Text style={styles.footerTotal}>Total: ${totalPrice}</Text>
                        <Text style={styles.footerItems}>{quantity} item(s)</Text>
                    </View>
                    <View style={styles.footerButtons}>
                        {!isInCart && (
                            <TouchableOpacity
                                style={styles.cartButton}
                                onPress={handleAddToCart}
                            >
                                <Ionicons name="cart-outline" size={22} color="#D4AF37" />
                                <Text style={styles.cartButtonText}>Add to Cart</Text>
                            </TouchableOpacity>
                        )}
                        <TouchableOpacity
                            style={styles.buyButton}
                            onPress={handleBuyNow}
                        >
                            <Ionicons name="flash" size={22} color="#FFFFFF" />
                            <Text style={styles.buyButtonText}>Buy Now</Text>
                        </TouchableOpacity>
                    </View>
                </View>
            )}
        </SafeAreaView>
    );
}

const styles = StyleSheet.create({
    safeArea: {
        flex: 1,
        backgroundColor: '#F8F9FA',
    },
    centered: {
        flex: 1,
        justifyContent: 'center',
        alignItems: 'center',
        padding: 20,
    },
    errorText: {
        fontSize: 16,
        color: '#DC3545',
        textAlign: 'center',
        marginBottom: 20,
    },
    retryButton: {
        backgroundColor: '#D4AF37',
        paddingHorizontal: 24,
        paddingVertical: 12,
        borderRadius: 8,
    },
    retryButtonText: {
        color: '#FFFFFF',
        fontWeight: '600',
        fontSize: 16,
    },
    scrollView: {
        flex: 1,
    },
    imageContainer: {
        backgroundColor: '#FFFFFF',
        paddingVertical: 16,
    },
    mainImage: {
        width: width,
        height: width,
        backgroundColor: '#FFFFFF',
    },
    placeholderImage: {
        justifyContent: 'center',
        alignItems: 'center',
        backgroundColor: '#F5F5F5',
    },
    imageGallery: {
        paddingHorizontal: 16,
        marginTop: 12,
    },
    thumbnail: {
        width: 80,
        height: 80,
        borderRadius: 8,
        marginRight: 8,
        borderWidth: 2,
        borderColor: 'transparent',
        overflow: 'hidden',
    },
    selectedThumbnail: {
        borderColor: '#D4AF37',
    },
    thumbnailImage: {
        width: '100%',
        height: '100%',
    },
    infoContainer: {
        padding: 16,
        backgroundColor: '#FFFFFF',
        marginTop: 8,
    },
    stockContainer: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
        marginBottom: 12,
    },
    inStockBadge: {
        flexDirection: 'row',
        alignItems: 'center',
        backgroundColor: '#E8F5E9',
        paddingHorizontal: 12,
        paddingVertical: 6,
        borderRadius: 6,
    },
    inStockText: {
        fontSize: 14,
        color: '#28A745',
        fontWeight: '500',
        marginLeft: 6,
    },
    outOfStockBadge: {
        flexDirection: 'row',
        alignItems: 'center',
        backgroundColor: '#FFE6E6',
        paddingHorizontal: 12,
        paddingVertical: 6,
        borderRadius: 6,
    },
    outOfStockText: {
        fontSize: 14,
        color: '#DC3545',
        fontWeight: '500',
        marginLeft: 6,
    },
    featuredBadge: {
        flexDirection: 'row',
        alignItems: 'center',
        backgroundColor: '#D4AF37',
        paddingHorizontal: 10,
        paddingVertical: 5,
        borderRadius: 4,
    },
    featuredText: {
        fontSize: 12,
        color: '#FFFFFF',
        fontWeight: '600',
        marginLeft: 4,
    },
    productName: {
        fontSize: 24,
        fontWeight: '700',
        color: '#1A1A1A',
        marginBottom: 12,
        lineHeight: 30,
    },
    priceContainer: {
        marginBottom: 16,
    },
    price: {
        fontSize: 28,
        fontWeight: '800',
        color: '#D4AF37',
        marginBottom: 4,
    },
    unitPrice: {
        fontSize: 14,
        color: '#666',
    },
    ratingContainer: {
        flexDirection: 'row',
        alignItems: 'center',
        marginBottom: 20,
    },
    stars: {
        flexDirection: 'row',
        marginRight: 8,
    },
    ratingText: {
        fontSize: 14,
        color: '#666',
    },
    section: {
        marginBottom: 24,
    },
    sectionTitle: {
        fontSize: 18,
        fontWeight: '600',
        color: '#1A1A1A',
        marginBottom: 12,
    },
    description: {
        fontSize: 16,
        color: '#666',
        lineHeight: 24,
    },
    attributesContainer: {
        backgroundColor: '#F8F9FA',
        borderRadius: 8,
        padding: 12,
    },
    attributeRow: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        paddingVertical: 8,
        borderBottomWidth: 1,
        borderBottomColor: '#E0E0E0',
    },
    attributeRowLast: {
        borderBottomWidth: 0,
    },
    attributeName: {
        fontSize: 14,
        color: '#666',
        flex: 1,
    },
    attributeValue: {
        fontSize: 14,
        fontWeight: '500',
        color: '#1A1A1A',
        flex: 2,
        textAlign: 'right',
    },
    sellerInfo: {
        flexDirection: 'row',
        alignItems: 'center',
        backgroundColor: '#F8F9FA',
        borderRadius: 8,
        padding: 16,
    },
    sellerAvatar: {
        width: 48,
        height: 48,
        borderRadius: 24,
        backgroundColor: '#FFF8E1',
        justifyContent: 'center',
        alignItems: 'center',
        marginRight: 12,
    },
    sellerDetails: {
        flex: 1,
    },
    sellerName: {
        fontSize: 16,
        fontWeight: '600',
        color: '#1A1A1A',
        marginBottom: 4,
    },
    sellerLocation: {
        fontSize: 13,
        color: '#666',
        marginBottom: 2,
    },
    sellerContact: {
        fontSize: 13,
        color: '#666',
    },
    contactButton: {
        backgroundColor: '#D4AF37',
        paddingHorizontal: 16,
        paddingVertical: 8,
        borderRadius: 6,
    },
    contactButtonText: {
        color: '#FFFFFF',
        fontWeight: '600',
        fontSize: 14,
    },
    quantitySection: {
        marginBottom: 24,
    },
    quantityContainer: {
        flexDirection: 'row',
        alignItems: 'center',
    },
    quantityButton: {
        width: 40,
        height: 40,
        borderRadius: 8,
        backgroundColor: '#F5F5F5',
        justifyContent: 'center',
        alignItems: 'center',
    },
    quantityText: {
        fontSize: 18,
        fontWeight: '600',
        color: '#1A1A1A',
        marginHorizontal: 16,
        minWidth: 40,
        textAlign: 'center',
    },
    quantityStock: {
        fontSize: 14,
        color: '#666',
        marginLeft: 16,
    },
    footer: {
        backgroundColor: '#FFFFFF',
        padding: 16,
        borderTopWidth: 1,
        borderTopColor: '#E0E0E0',
        shadowColor: '#000',
        shadowOffset: { width: 0, height: -2 },
        shadowOpacity: 0.1,
        shadowRadius: 4,
        elevation: 5,
    },
    footerInfo: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
        marginBottom: 12,
    },
    footerTotal: {
        fontSize: 18,
        fontWeight: '700',
        color: '#1A1A1A',
    },
    footerItems: {
        fontSize: 14,
        color: '#666',
    },
    footerButtons: {
        flexDirection: 'row',
        gap: 12,
    },
    cartButton: {
        flex: 1,
        flexDirection: 'row',
        alignItems: 'center',
        justifyContent: 'center',
        backgroundColor: '#FFF8E1',
        paddingVertical: 14,
        borderRadius: 8,
        borderWidth: 1,
        borderColor: '#D4AF37',
    },
    cartButtonText: {
        fontSize: 16,
        fontWeight: '600',
        color: '#D4AF37',
        marginLeft: 8,
    },
    buyButton: {
        flex: 1,
        flexDirection: 'row',
        alignItems: 'center',
        justifyContent: 'center',
        backgroundColor: '#D4AF37',
        paddingVertical: 14,
        borderRadius: 8,
    },
    buyButtonText: {
        fontSize: 16,
        fontWeight: '600',
        color: '#FFFFFF',
        marginLeft: 8,
    },
    headerButtons: {
        flexDirection: 'row',
        alignItems: 'center',
    },
    headerButton: {
        marginLeft: 16,
        padding: 4,
    },
});
