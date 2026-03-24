// app/product/[productId].tsx
import React, { useState, useEffect } from 'react';
import {
    View,
    Text,
    StyleSheet,
    ScrollView,
    Image,
    TouchableOpacity,
    StatusBar,
    ActivityIndicator,
    Alert,
} from 'react-native';
import { useLocalSearchParams, useRouter, Stack } from 'expo-router';
import { Ionicons } from '@expo/vector-icons';
import api, { Product } from '@/components/services/api';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { SafeAreaView } from 'react-native-safe-area-context';

export default function ProductDetailsScreen() {
    const { productId } = useLocalSearchParams<{ productId: string }>();
    const router = useRouter();

    const [product, setProduct] = useState<Product | null>(null);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState<string | null>(null);
    const [quantity, setQuantity] = useState(1);
    const [selectedImageIndex, setSelectedImageIndex] = useState(0);
    const [isAuthenticated, setIsAuthenticated] = useState(false);

    useEffect(() => {
        checkAuth();
        fetchProductDetails();
    }, [productId]);

    const checkAuth = async () => {
        try {
            const token = await AsyncStorage.getItem('authToken');
            if (token) {
                api.setToken(token);
                setIsAuthenticated(true);
            }
        } catch (error) {
            console.error('Check auth error:', error);
        }
    };

    const fetchProductDetails = async () => {
        if (!productId) return;

        try {
            setLoading(true);
            setError(null);

            const response = await api.getProductById(productId as string);

            if (response.success && response.data) {
                setProduct(response.data);
            } else {
                setError(response.message || 'Failed to load product details');
            }
        } catch (error: any) {
            console.error('Fetch product error:', error);
            setError(error.message || 'An error occurred while fetching product details');
        } finally {
            setLoading(false);
        }
    };

    const handleAddToCart = async () => {
        if (!isAuthenticated) {
            Alert.alert(
                'Login Required',
                'Please login to add items to your cart',
                [
                    { text: 'Cancel', style: 'cancel' },
                    {
                        text: 'Login',
                        onPress: () => router.push('/login')
                    }
                ]
            );
            return;
        }

        if (!product) return;

        try {
            // TODO: Implement add to cart API call
            console.log('Added to cart:', {
                productId: product._id,
                quantity,
                price: product.price,
                name: product.name
            });

            Alert.alert(
                'Success',
                'Item added to cart successfully',
                [
                    {
                        text: 'View Cart',
                        onPress: () => router.push('/cart')
                    },
                    {
                        text: 'Continue Shopping',
                        style: 'cancel'
                    }
                ]
            );
        } catch (error: any) {
            Alert.alert('Error', error.message || 'Failed to add item to cart');
        }
    };

    const handleBuyNow = () => {
        if (!isAuthenticated) {
            Alert.alert(
                'Login Required',
                'Please login to continue with checkout',
                [
                    { text: 'Cancel', style: 'cancel' },
                    {
                        text: 'Login',
                        onPress: () => router.push('/login')
                    }
                ]
            );
            return;
        }

        if (!product) return;

        router.push({
            pathname: '/checkout',
            params: {
                productId: product._id,
                quantity: quantity.toString()
            }
        });
    };

    const handleContactSeller = () => {
        if (!product) return;

        // Navigate to seller profile
        router.push({
            pathname: '/seller/[sellerId]',
            params: {
                sellerId: product.email, // Using email as seller ID
                productId: product._id
            }
        });
    };

    const formatPrice = (price: number) => {
        return `$${price.toLocaleString()}`;
    };

    const renderImageGallery = () => {
        if (!product) return null;

        const images = product.images?.length ? product.images : [];

        return (
            <View>
                {/* Main Image */}
                <View style={styles.imageContainer}>
                    {images.length > 0 && images[selectedImageIndex] ? (
                        <Image
                            source={{ uri: images[selectedImageIndex].url }}
                            style={styles.productImage}
                            resizeMode="contain"
                        />
                    ) : (
                        <View style={styles.imagePlaceholder}>
                            <Ionicons name="image-outline" size={48} color="#CCC" />
                            <Text style={styles.placeholderText}>No image available</Text>
                        </View>
                    )}
                </View>

                {/* Thumbnail Gallery */}
                {images.length > 1 && (
                    <ScrollView
                        horizontal
                        showsHorizontalScrollIndicator={false}
                        style={styles.thumbnailContainer}
                        contentContainerStyle={styles.thumbnailContent}
                    >
                        {images.map((image, index) => (
                            <TouchableOpacity
                                key={index}
                                onPress={() => setSelectedImageIndex(index)}
                                style={[
                                    styles.thumbnailWrapper,
                                    selectedImageIndex === index && styles.selectedThumbnail
                                ]}
                            >
                                <Image
                                    source={{ uri: image.url }}
                                    style={styles.thumbnailImage}
                                    resizeMode="cover"
                                />
                            </TouchableOpacity>
                        ))}
                    </ScrollView>
                )}
            </View>
        );
    };

    const renderSpecifications = () => {
        if (!product?.attributes?.length) return null;

        return (
            <View style={styles.section}>
                <Text style={styles.sectionTitle}>Specifications</Text>
                {product.attributes.map((attr, index) => (
                    <View key={index} style={styles.specRow}>
                        <Text style={styles.specLabel}>{attr.name}</Text>
                        <Text style={styles.specValue}>{attr.value}</Text>
                    </View>
                ))}
            </View>
        );
    };

    // Loading State
    if (loading) {
        return (
            <SafeAreaView style={styles.safeArea}>
                <Stack.Screen
                    options={{
                        headerShown: true,
                        headerTitle: 'Product Details',
                        headerBackTitle: 'Back',
                        headerTintColor: '#D4AF37',
                        headerStyle: { backgroundColor: '#FFFFFF' },
                    }}
                />
                <View style={styles.loadingContainer}>
                    <ActivityIndicator size="large" color="#D4AF37" />
                    <Text style={styles.loadingText}>Loading product details...</Text>
                </View>
            </SafeAreaView>
        );
    }

    // Error State
    if (error || !product) {
        return (
            <SafeAreaView style={styles.safeArea}>
                <Stack.Screen
                    options={{
                        headerShown: true,
                        headerTitle: 'Error',
                        headerBackTitle: 'Back',
                        headerTintColor: '#D4AF37',
                        headerStyle: { backgroundColor: '#FFFFFF' },
                    }}
                />
                <View style={styles.errorContainer}>
                    <Ionicons name="alert-circle-outline" size={64} color="#FF6B6B" />
                    <Text style={styles.errorTitle}>Oops!</Text>
                    <Text style={styles.errorText}>{error || 'Product not found'}</Text>
                    <TouchableOpacity
                        style={styles.retryButton}
                        onPress={fetchProductDetails}
                    >
                        <Text style={styles.retryButtonText}>Try Again</Text>
                    </TouchableOpacity>
                    <TouchableOpacity
                        style={styles.goBackButton}
                        onPress={() => router.back()}
                    >
                        <Text style={styles.goBackText}>Go Back</Text>
                    </TouchableOpacity>
                </View>
            </SafeAreaView>
        );
    }

    return (
        <SafeAreaView style={styles.safeArea}>
            <StatusBar barStyle="dark-content" backgroundColor="#FFFFFF" />

            <Stack.Screen
                options={{
                    headerShown: true,
                    headerTitle: product.categoryName || 'Product Details',
                    headerBackTitle: 'Back',
                    headerTintColor: '#D4AF37',
                    headerStyle: { backgroundColor: '#FFFFFF' },
                    headerRight: () => (
                        <TouchableOpacity
                            style={styles.headerButton}
                            onPress={() => router.push('/cart')}
                        >
                            <Ionicons name="cart-outline" size={24} color="#D4AF37" />
                        </TouchableOpacity>
                    ),
                }}
            />

            <ScrollView
                showsVerticalScrollIndicator={false}
                contentContainerStyle={styles.scrollContent}
            >
                {/* Image Gallery */}
                {renderImageGallery()}

                {/* Product Info */}
                <View style={styles.contentContainer}>
                    {/* Product Header */}
                    <View style={styles.productHeader}>
                        <Text style={styles.productName}>{product.name}</Text>

                        <View style={styles.ratingContainer}>
                            <Ionicons name="star" size={16} color="#FFC107" />
                            <Text style={styles.ratingText}>
                                {product.rating?.average?.toFixed(1) || '0.0'}
                            </Text>
                            <Text style={styles.reviewCount}>
                                ({product.rating?.count || 0} reviews)
                            </Text>
                        </View>

                        <Text style={styles.productPrice}>
                            {formatPrice(product.price)}
                        </Text>
                    </View>

                    {/* Stock Status */}
                    <View style={styles.stockContainer}>
                        {product.stock > 0 ? (
                            <>
                                <View style={styles.inStockBadge}>
                                    <Ionicons name="checkmark-circle" size={16} color="#4CAF50" />
                                    <Text style={styles.inStockText}>
                                        In Stock ({product.stock} available)
                                    </Text>
                                </View>
                            </>
                        ) : (
                            <View style={styles.outOfStockBadge}>
                                <Ionicons name="close-circle" size={16} color="#FF6B6B" />
                                <Text style={styles.outOfStockText}>Out of Stock</Text>
                            </View>
                        )}
                    </View>

                    {/* Category */}
                    {product.category && (
                        <View style={styles.categoryBadge}>
                            <Ionicons
                                /*  name={product.category.icon || 'pricetag'} */
                                size={14}
                                color={product.category.color || '#D4AF37'}
                            />
                            <Text style={styles.categoryText}>
                                {product.category.title}
                            </Text>
                        </View>
                    )}

                    {/* Seller Info */}
                    <TouchableOpacity
                        style={styles.sellerCard}
                        onPress={handleContactSeller}
                    >
                        <View style={styles.sellerAvatar}>
                            <Text style={styles.sellerAvatarText}>
                                {product.companyName?.charAt(0) || product.email?.charAt(0) || 'S'}
                            </Text>
                        </View>
                        <View style={styles.sellerInfo}>
                            <Text style={styles.sellerName}>
                                {product.companyName || 'Unknown Seller'}
                            </Text>
                            <View style={styles.sellerLocation}>
                                <Ionicons name="location-outline" size={12} color="#666" />
                                <Text style={styles.sellerLocationText} numberOfLines={1}>
                                    {product.location || 'Location not specified'}
                                </Text>
                            </View>
                            <View style={styles.sellerContact}>
                                <Ionicons name="call-outline" size={12} color="#666" />
                                <Text style={styles.sellerContactText}>
                                    {product.phoneNumber}
                                </Text>
                            </View>
                        </View>
                        <Ionicons name="chevron-forward" size={20} color="#D4AF37" />
                    </TouchableOpacity>

                    {/* Product Description */}
                    <View style={styles.section}>
                        <Text style={styles.sectionTitle}>Description</Text>
                        <Text style={styles.description}>
                            {product.description || 'No description available.'}
                        </Text>
                    </View>

                    {/* Specifications */}
                    {renderSpecifications()}

                    {/* Quantity Selector - Only show if in stock */}
                    {product.stock > 0 && (
                        <View style={styles.section}>
                            <Text style={styles.sectionTitle}>Quantity</Text>
                            <View style={styles.quantityContainer}>
                                <TouchableOpacity
                                    style={styles.quantityButton}
                                    onPress={() => setQuantity(Math.max(1, quantity - 1))}
                                    disabled={quantity <= 1}
                                >
                                    <Ionicons
                                        name="remove"
                                        size={20}
                                        color={quantity <= 1 ? '#CCC' : '#D4AF37'}
                                    />
                                </TouchableOpacity>
                                <Text style={styles.quantity}>{quantity}</Text>
                                <TouchableOpacity
                                    style={styles.quantityButton}
                                    onPress={() => setQuantity(Math.min(product.stock, quantity + 1))}
                                    disabled={quantity >= product.stock}
                                >
                                    <Ionicons
                                        name="add"
                                        size={20}
                                        color={quantity >= product.stock ? '#CCC' : '#D4AF37'}
                                    />
                                </TouchableOpacity>
                                <Text style={styles.stockLimit}>Max: {product.stock}</Text>
                            </View>
                        </View>
                    )}

                    {/* Additional Info */}
                    <View style={styles.metaContainer}>
                        <View style={styles.metaItem}>
                            <Ionicons name="time-outline" size={14} color="#666" />
                            <Text style={styles.metaText}>
                                Listed: {new Date(product.createdAt).toLocaleDateString()}
                            </Text>
                        </View>
                        <View style={styles.metaItem}>
                            <Ionicons name="eye-outline" size={14} color="#666" />
                            <Text style={styles.metaText}>
                                {product.views || 0} views
                            </Text>
                        </View>
                    </View>
                </View>
            </ScrollView>

            {/* Bottom Actions */}
            {product.stock > 0 && (
                <View style={styles.bottomBar}>
                    <TouchableOpacity
                        style={styles.contactButton}
                        onPress={handleContactSeller}
                    >
                        <Ionicons name="chatbubble-outline" size={20} color="#D4AF37" />
                        <Text style={styles.contactButtonText}>Contact</Text>
                    </TouchableOpacity>

                    <TouchableOpacity
                        style={styles.addToCartButton}
                        onPress={handleAddToCart}
                    >
                        <Ionicons name="cart-outline" size={20} color="#D4AF37" />
                        <Text style={styles.addToCartText}>Add to Cart</Text>
                    </TouchableOpacity>

                    <TouchableOpacity
                        style={styles.buyNowButton}
                        onPress={handleBuyNow}
                    >
                        <Text style={styles.buyNowText}>Buy Now</Text>
                    </TouchableOpacity>
                </View>
            )}

            {/* Out of Stock Message */}
            {product.stock <= 0 && (
                <View style={styles.outOfStockBar}>
                    <Ionicons name="information-circle" size={20} color="#FF6B6B" />
                    <Text style={styles.outOfStockBarText}>This product is currently out of stock</Text>
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
    scrollContent: {
        flexGrow: 1,
    },
    headerButton: {
        marginRight: 16,
    },
    loadingContainer: {
        flex: 1,
        justifyContent: 'center',
        alignItems: 'center',
        backgroundColor: '#F8F9FA',
    },
    loadingText: {
        marginTop: 12,
        fontSize: 14,
        color: '#666',
    },
    errorContainer: {
        flex: 1,
        justifyContent: 'center',
        alignItems: 'center',
        padding: 24,
        backgroundColor: '#F8F9FA',
    },
    errorTitle: {
        fontSize: 20,
        fontWeight: '700',
        color: '#1A1A1A',
        marginTop: 16,
        marginBottom: 8,
    },
    errorText: {
        fontSize: 14,
        color: '#666',
        textAlign: 'center',
        marginBottom: 24,
    },
    retryButton: {
        backgroundColor: '#D4AF37',
        paddingHorizontal: 32,
        paddingVertical: 12,
        borderRadius: 8,
        marginBottom: 12,
    },
    retryButtonText: {
        color: '#FFFFFF',
        fontSize: 14,
        fontWeight: '600',
    },
    goBackButton: {
        paddingHorizontal: 32,
        paddingVertical: 12,
    },
    goBackText: {
        color: '#666',
        fontSize: 14,
    },
    imageContainer: {
        backgroundColor: '#FFFFFF',
        height: 300,
        justifyContent: 'center',
        alignItems: 'center',
    },
    productImage: {
        width: '100%',
        height: '100%',
        resizeMode: 'contain',
    },
    imagePlaceholder: {
        width: '100%',
        height: '100%',
        justifyContent: 'center',
        alignItems: 'center',
        backgroundColor: '#F0F0F0',
    },
    placeholderText: {
        marginTop: 8,
        fontSize: 12,
        color: '#999',
    },
    thumbnailContainer: {
        backgroundColor: '#FFFFFF',
        paddingVertical: 12,
    },
    thumbnailContent: {
        paddingHorizontal: 16,
    },
    thumbnailWrapper: {
        marginRight: 12,
        borderRadius: 8,
        borderWidth: 2,
        borderColor: 'transparent',
        overflow: 'hidden',
    },
    selectedThumbnail: {
        borderColor: '#D4AF37',
    },
    thumbnailImage: {
        width: 60,
        height: 60,
    },
    contentContainer: {
        padding: 16,
    },
    productHeader: {
        backgroundColor: '#FFFFFF',
        padding: 16,
        borderRadius: 12,
        marginBottom: 12,
    },
    productName: {
        fontSize: 20,
        fontWeight: '600',
        color: '#1A1A1A',
        marginBottom: 8,
    },
    ratingContainer: {
        flexDirection: 'row',
        alignItems: 'center',
        marginBottom: 8,
    },
    ratingText: {
        fontSize: 14,
        fontWeight: '600',
        color: '#1A1A1A',
        marginLeft: 4,
    },
    reviewCount: {
        fontSize: 12,
        color: '#666',
        marginLeft: 4,
    },
    productPrice: {
        fontSize: 24,
        fontWeight: '700',
        color: '#D4AF37',
    },
    stockContainer: {
        flexDirection: 'row',
        alignItems: 'center',
        marginBottom: 12,
    },
    inStockBadge: {
        flexDirection: 'row',
        alignItems: 'center',
        backgroundColor: 'rgba(76, 175, 80, 0.1)',
        paddingHorizontal: 12,
        paddingVertical: 6,
        borderRadius: 16,
    },
    inStockText: {
        fontSize: 12,
        color: '#4CAF50',
        fontWeight: '600',
        marginLeft: 4,
    },
    outOfStockBadge: {
        flexDirection: 'row',
        alignItems: 'center',
        backgroundColor: 'rgba(255, 107, 107, 0.1)',
        paddingHorizontal: 12,
        paddingVertical: 6,
        borderRadius: 16,
    },
    outOfStockText: {
        fontSize: 12,
        color: '#FF6B6B',
        fontWeight: '600',
        marginLeft: 4,
    },
    categoryBadge: {
        flexDirection: 'row',
        alignItems: 'center',
        marginBottom: 16,
    },
    categoryText: {
        fontSize: 12,
        color: '#666',
        marginLeft: 4,
    },
    sellerCard: {
        backgroundColor: '#FFFFFF',
        padding: 16,
        borderRadius: 12,
        flexDirection: 'row',
        alignItems: 'center',
        marginBottom: 16,
    },
    sellerAvatar: {
        width: 48,
        height: 48,
        borderRadius: 24,
        backgroundColor: '#D4AF37',
        justifyContent: 'center',
        alignItems: 'center',
        marginRight: 12,
    },
    sellerAvatarText: {
        color: '#FFFFFF',
        fontSize: 18,
        fontWeight: '600',
    },
    sellerInfo: {
        flex: 1,
    },
    sellerName: {
        fontSize: 16,
        fontWeight: '600',
        color: '#1A1A1A',
        marginBottom: 4,
    },
    sellerLocation: {
        flexDirection: 'row',
        alignItems: 'center',
        marginBottom: 2,
    },
    sellerLocationText: {
        fontSize: 12,
        color: '#666',
        marginLeft: 4,
    },
    sellerContact: {
        flexDirection: 'row',
        alignItems: 'center',
    },
    sellerContactText: {
        fontSize: 12,
        color: '#666',
        marginLeft: 4,
    },
    section: {
        backgroundColor: '#FFFFFF',
        padding: 16,
        borderRadius: 12,
        marginBottom: 16,
    },
    sectionTitle: {
        fontSize: 16,
        fontWeight: '600',
        color: '#1A1A1A',
        marginBottom: 12,
    },
    description: {
        fontSize: 14,
        color: '#666',
        lineHeight: 20,
    },
    specRow: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        paddingVertical: 8,
        borderBottomWidth: 1,
        borderBottomColor: 'rgba(0,0,0,0.05)',
    },
    specLabel: {
        fontSize: 14,
        color: '#666',
    },
    specValue: {
        fontSize: 14,
        color: '#1A1A1A',
        fontWeight: '500',
    },
    quantityContainer: {
        flexDirection: 'row',
        alignItems: 'center',
    },
    quantityButton: {
        width: 40,
        height: 40,
        borderRadius: 20,
        borderWidth: 1,
        borderColor: '#D4AF37',
        justifyContent: 'center',
        alignItems: 'center',
    },
    quantity: {
        fontSize: 16,
        fontWeight: '600',
        color: '#1A1A1A',
        marginHorizontal: 20,
    },
    stockLimit: {
        fontSize: 12,
        color: '#666',
        marginLeft: 12,
    },
    metaContainer: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        paddingHorizontal: 16,
        paddingBottom: 16,
    },
    metaItem: {
        flexDirection: 'row',
        alignItems: 'center',
    },
    metaText: {
        fontSize: 12,
        color: '#666',
        marginLeft: 4,
    },
    bottomBar: {
        flexDirection: 'row',
        padding: 16,
        backgroundColor: '#FFFFFF',
        borderTopWidth: 1,
        borderTopColor: 'rgba(0,0,0,0.05)',
    },
    contactButton: {
        width: 80,
        justifyContent: 'center',
        alignItems: 'center',
        marginRight: 12,
        borderWidth: 1,
        borderColor: '#D4AF37',
        borderRadius: 8,
        paddingVertical: 12,
    },
    contactButtonText: {
        fontSize: 12,
        color: '#D4AF37',
        marginTop: 2,
    },
    addToCartButton: {
        flex: 1,
        flexDirection: 'row',
        justifyContent: 'center',
        alignItems: 'center',
        paddingVertical: 12,
        marginRight: 12,
        borderRadius: 8,
        borderWidth: 1,
        borderColor: '#D4AF37',
    },
    addToCartText: {
        fontSize: 14,
        fontWeight: '600',
        color: '#D4AF37',
        marginLeft: 8,
    },
    buyNowButton: {
        flex: 1,
        justifyContent: 'center',
        alignItems: 'center',
        paddingVertical: 12,
        borderRadius: 8,
        backgroundColor: '#D4AF37',
    },
    buyNowText: {
        fontSize: 14,
        fontWeight: '600',
        color: '#FFFFFF',
    },
    outOfStockBar: {
        flexDirection: 'row',
        alignItems: 'center',
        justifyContent: 'center',
        padding: 16,
        backgroundColor: '#FFE5E5',
        borderTopWidth: 1,
        borderTopColor: 'rgba(255, 107, 107, 0.2)',
    },
    outOfStockBarText: {
        fontSize: 14,
        color: '#FF6B6B',
        fontWeight: '500',
        marginLeft: 8,
    },
});