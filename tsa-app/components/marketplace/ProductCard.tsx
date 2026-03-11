// app/components/marketplace/ProductCard.tsx
import React, { useState } from 'react';
import {
    View,
    Text,
    TouchableOpacity,
    Image,
    StyleSheet,
    Dimensions,
} from 'react-native';
import { Product } from '@/components/services/api';
import Icon from 'react-native-vector-icons/MaterialIcons';
import Ionicons from '@expo/vector-icons/Ionicons';

interface ProductCardProps {
    product: Product;
    onPress: () => void;
    variant?: 'list' | 'grid';
}

const { width } = Dimensions.get('window');

const ProductCard: React.FC<ProductCardProps> = ({ product, onPress, variant = 'list' }) => {
    const [imageError, setImageError] = useState(false);

    const primaryImage = product.images?.[0]?.url;
    const price = typeof product.price === 'number' ? product.price.toFixed(2) : '0.00';
    const rating = product.rating?.average || 0;
    const ratingCount = product.rating?.count || 0;
    const isInStock = product.stock > 0;

    if (variant === 'grid') {
        return (
            <TouchableOpacity style={styles.gridCard} onPress={onPress} activeOpacity={0.7}>
                {/* Image */}
                <View style={styles.gridImageContainer}>
                    {primaryImage && !imageError ? (
                        <Image
                            source={{ uri: primaryImage }}
                            style={styles.gridImage}
                            resizeMode="cover"
                            onError={() => setImageError(true)}
                        />
                    ) : (
                        <View style={[styles.gridImage, styles.gridPlaceholder]}>
                            <Icon name="image" size={32} color="#999" />
                        </View>
                    )}

                    {/* Stock Status */}
                    {!isInStock && (
                        <View style={styles.outOfStockBadge}>
                            <Text style={styles.outOfStockText}>Out of Stock</Text>
                        </View>
                    )}

                    {/* Featured Badge */}
                    {product.isFeatured && (
                        <View style={styles.featuredBadge}>
                            <Ionicons name="star" size={12} color="#FFFFFF" />
                        </View>
                    )}
                </View>

                {/* Product Info */}
                <View style={styles.gridInfo}>
                    <Text style={styles.gridName} numberOfLines={2}>
                        {product.name}
                    </Text>

                    <Text style={styles.gridPrice}>${price}</Text>

                    {rating > 0 && (
                        <View style={styles.ratingContainer}>
                            <Ionicons name="star" size={12} color="#FFD700" />
                            <Text style={styles.ratingText}>{rating.toFixed(1)}</Text>
                            <Text style={styles.ratingCount}>({ratingCount})</Text>
                        </View>
                    )}

                    {/* Quick Actions */}
                    <View style={styles.quickActions}>
                        <TouchableOpacity style={styles.quickActionButton}>
                            <Ionicons name="heart-outline" size={16} color="#666" />
                        </TouchableOpacity>
                        <TouchableOpacity style={styles.quickActionButton}>
                            <Ionicons name="cart-outline" size={16} color="#666" />
                        </TouchableOpacity>
                    </View>
                </View>
            </TouchableOpacity>
        );
    }

    // List View
    return (
        <TouchableOpacity style={styles.listCard} onPress={onPress} activeOpacity={0.7}>
            {/* Image */}
            <View style={styles.listImageContainer}>
                {primaryImage && !imageError ? (
                    <Image
                        source={{ uri: primaryImage }}
                        style={styles.listImage}
                        resizeMode="cover"
                        onError={() => setImageError(true)}
                    />
                ) : (
                    <View style={[styles.listImage, styles.listPlaceholder]}>
                        <Icon name="image" size={40} color="#999" />
                    </View>
                )}

                {/* Stock Status */}
                {!isInStock && (
                    <View style={[styles.outOfStockBadge, styles.listStockBadge]}>
                        <Text style={styles.outOfStockText}>Out of Stock</Text>
                    </View>
                )}
            </View>

            {/* Product Info */}
            <View style={styles.listInfo}>
                <View style={styles.listHeader}>
                    <View style={styles.listTitleContainer}>
                        <Text style={styles.listName} numberOfLines={2}>
                            {product.name}
                        </Text>
                        {product.isFeatured && (
                            <View style={styles.featuredBadgeList}>
                                <Ionicons name="star" size={12} color="#FFFFFF" />
                                <Text style={styles.featuredText}>Featured</Text>
                            </View>
                        )}
                    </View>

                    <Text style={styles.listPrice}>${price}</Text>
                </View>

                <Text style={styles.listDescription} numberOfLines={2}>
                    {product.description}
                </Text>

                {/* Rating and Location */}
                <View style={styles.listDetails}>
                    {rating > 0 && (
                        <View style={styles.ratingContainer}>
                            <Ionicons name="star" size={14} color="#FFD700" />
                            <Text style={styles.ratingText}>{rating.toFixed(1)}</Text>
                            <Text style={styles.ratingCount}>({ratingCount})</Text>
                        </View>
                    )}

                    <View style={styles.locationContainer}>
                        <Ionicons name="location-outline" size={12} color="#666" />
                        <Text style={styles.locationText} numberOfLines={1}>
                            {product.location}
                        </Text>
                    </View>
                </View>

                {/* Seller Info */}
                {product.companyName && (
                    <View style={styles.sellerContainer}>
                        <Text style={styles.sellerText}>Sold by: {product.companyName}</Text>
                    </View>
                )}

                {/* Quick Actions */}
                <View style={styles.listActions}>
                    <TouchableOpacity style={styles.wishlistButton}>
                        <Ionicons name="heart-outline" size={18} color="#666" />
                        <Text style={styles.actionText}>Wishlist</Text>
                    </TouchableOpacity>

                    <TouchableOpacity style={styles.cartButton}>
                        <Ionicons name="cart-outline" size={18} color="#D4AF37" />
                        <Text style={[styles.actionText, styles.cartText]}>Add to Cart</Text>
                    </TouchableOpacity>
                </View>
            </View>
        </TouchableOpacity>
    );
};

const styles = StyleSheet.create({
    // Grid Styles
    gridCard: {
        width: '100%',
        backgroundColor: '#FFFFFF',
        borderRadius: 12,
        overflow: 'hidden',
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 2 },
        shadowOpacity: 0.05,
        shadowRadius: 4,
        elevation: 2,
        borderWidth: 1,
        borderColor: '#F0F0F0',
    },
    gridImageContainer: {
        position: 'relative',
        width: '100%',
        height: 160,
    },
    gridImage: {
        width: '100%',
        height: '100%',
    },
    gridPlaceholder: {
        backgroundColor: '#F5F5F5',
        justifyContent: 'center',
        alignItems: 'center',
    },
    gridInfo: {
        padding: 12,
    },
    gridName: {
        fontSize: 14,
        fontWeight: '600',
        color: '#1A1A1A',
        marginBottom: 6,
        lineHeight: 18,
    },
    gridPrice: {
        fontSize: 16,
        fontWeight: '700',
        color: '#D4AF37',
        marginBottom: 6,
    },

    // List Styles
    listCard: {
        flexDirection: 'row',
        backgroundColor: '#FFFFFF',
        borderRadius: 12,
        marginHorizontal: 16,
        marginBottom: 12,
        padding: 12,
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 2 },
        shadowOpacity: 0.05,
        shadowRadius: 4,
        elevation: 2,
        borderWidth: 1,
        borderColor: '#F0F0F0',
    },
    listImageContainer: {
        position: 'relative',
        width: 100,
        height: 100,
        marginRight: 12,
    },
    listImage: {
        width: '100%',
        height: '100%',
        borderRadius: 8,
    },
    listPlaceholder: {
        backgroundColor: '#F5F5F5',
        justifyContent: 'center',
        alignItems: 'center',
        borderRadius: 8,
    },
    listInfo: {
        flex: 1,
    },
    listHeader: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'flex-start',
        marginBottom: 6,
    },
    listTitleContainer: {
        flex: 1,
        marginRight: 8,
    },
    listName: {
        fontSize: 16,
        fontWeight: '600',
        color: '#1A1A1A',
        lineHeight: 20,
        marginBottom: 4,
    },
    listPrice: {
        fontSize: 18,
        fontWeight: '700',
        color: '#D4AF37',
    },
    listDescription: {
        fontSize: 13,
        color: '#666',
        lineHeight: 18,
        marginBottom: 8,
    },
    listDetails: {
        flexDirection: 'row',
        alignItems: 'center',
        marginBottom: 8,
    },

    // Common Styles
    outOfStockBadge: {
        position: 'absolute',
        top: 8,
        left: 8,
        backgroundColor: 'rgba(220, 53, 69, 0.9)',
        paddingHorizontal: 8,
        paddingVertical: 4,
        borderRadius: 4,
    },
    listStockBadge: {
        top: 4,
        left: 4,
    },
    outOfStockText: {
        fontSize: 10,
        fontWeight: '600',
        color: '#FFFFFF',
    },
    featuredBadge: {
        position: 'absolute',
        top: 8,
        right: 8,
        backgroundColor: '#D4AF37',
        width: 24,
        height: 24,
        borderRadius: 12,
        justifyContent: 'center',
        alignItems: 'center',
    },
    featuredBadgeList: {
        flexDirection: 'row',
        alignItems: 'center',
        backgroundColor: '#D4AF37',
        paddingHorizontal: 8,
        paddingVertical: 2,
        borderRadius: 4,
        alignSelf: 'flex-start',
        marginBottom: 4,
    },
    featuredText: {
        fontSize: 10,
        fontWeight: '600',
        color: '#FFFFFF',
        marginLeft: 4,
    },
    ratingContainer: {
        flexDirection: 'row',
        alignItems: 'center',
        marginRight: 12,
    },
    ratingText: {
        fontSize: 12,
        fontWeight: '600',
        color: '#1A1A1A',
        marginLeft: 4,
        marginRight: 2,
    },
    ratingCount: {
        fontSize: 11,
        color: '#666',
    },
    locationContainer: {
        flexDirection: 'row',
        alignItems: 'center',
        flex: 1,
    },
    locationText: {
        fontSize: 12,
        color: '#666',
        marginLeft: 4,
        flex: 1,
    },
    sellerContainer: {
        marginBottom: 8,
    },
    sellerText: {
        fontSize: 12,
        color: '#666',
        fontStyle: 'italic',
    },
    quickActions: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        marginTop: 8,
    },
    quickActionButton: {
        width: 32,
        height: 32,
        borderRadius: 16,
        backgroundColor: '#F5F5F5',
        justifyContent: 'center',
        alignItems: 'center',
    },
    listActions: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        marginTop: 8,
    },
    wishlistButton: {
        flexDirection: 'row',
        alignItems: 'center',
        paddingHorizontal: 12,
        paddingVertical: 8,
        borderRadius: 6,
        backgroundColor: '#F5F5F5',
    },
    cartButton: {
        flexDirection: 'row',
        alignItems: 'center',
        paddingHorizontal: 16,
        paddingVertical: 8,
        borderRadius: 6,
        backgroundColor: '#FFF8E1',
        borderWidth: 1,
        borderColor: '#D4AF37',
    },
    actionText: {
        fontSize: 13,
        fontWeight: '500',
        marginLeft: 6,
    },
    cartText: {
        color: '#D4AF37',
    },
});

export default ProductCard;