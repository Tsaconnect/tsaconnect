import React, { useCallback, useState, useRef } from 'react';
import {
    StyleSheet,
    Text,
    View,
    FlatList,
    TouchableOpacity,
    Alert,
    Image,
    RefreshControl,
    Animated,
    Dimensions,
} from 'react-native';
import { COLORS, SIZES, SHADOWS, FONTS } from '@/constants/theme';
import Icon from '@expo/vector-icons/MaterialIcons';
import api, { Category } from '@/components/services/api';
import { router, useFocusEffect } from 'expo-router';

const { width } = Dimensions.get('window');
const ITEM_HEIGHT = 110; // Adjusted for padding

const CategoryDisplayScreen = () => {
    const [categories, setCategories] = useState<Category[]>([]);
    const [loading, setLoading] = useState(true);
    const [refreshing, setRefreshing] = useState(false);
    const [searchQuery, setSearchQuery] = useState('');
    const [selectedType, setSelectedType] = useState<string | null>(null);

    const fadeAnim = useRef(new Animated.Value(0)).current;
    const slideAnim = useRef(new Animated.Value(50)).current;
    const scrollY = useRef(new Animated.Value(0)).current;

    const categoryTypes = [
        { label: 'All', value: null, icon: 'widgets', color: COLORS.primary },
        { label: 'Product', value: 'Product', icon: 'shopping-cart', color: '#0284C7' },
        { label: 'Service', value: 'Service', icon: 'handyman', color: '#16A34A' },
        { label: 'Both', value: 'Both', icon: 'category', color: '#9333EA' },
    ];

    const fetchCategories = async () => {
        try {
            const response = await api.getCategories({ active: true });
            if (response.success && response.data) {
                setCategories(response.data);
            } else {
                Alert.alert('Error', response.message || 'Failed to fetch categories');
            }
        } catch (error) {
            Alert.alert('Error', 'An unexpected error occurred');
        } finally {
            setLoading(false);
            setRefreshing(false);
        }
    };

    useFocusEffect(
        useCallback(() => {
            fetchCategories();
            Animated.parallel([
                Animated.timing(fadeAnim, { toValue: 1, duration: 600, useNativeDriver: true }),
                Animated.timing(slideAnim, { toValue: 0, duration: 600, useNativeDriver: true }),
            ]).start();
        }, [])
    );

    const onRefresh = () => {
        setRefreshing(true);
        fetchCategories();
    };

    const handleDelete = (id: string, name: string) => {
        Alert.alert(
            '🗑️ Delete Category',
            `Are you sure you want to delete "${name}"?`,
            [
                { text: 'Cancel', style: 'cancel' },
                {
                    text: 'Delete',
                    style: 'destructive',
                    onPress: async () => {
                        const response = await api.deleteCategory(id);
                        if (response.success) fetchCategories();
                    },
                },
            ]
        );
    };

    const handleEdit = (category: Category) => {
        router.push({
            pathname: '/admin/category/add',
            params: {
                id: category._id,
                mode: 'edit',
                title: category.title,
                type: category.type,
                description: category.description,
                isActive: category.isActive.toString(),
            },
        });
    };

    const getFirstLetter = (title: string) => title?.charAt(0)?.toUpperCase() || 'C';

    const getRandomColor = (seed: string) => {
        const colors = ['#FF6B6B', '#4ECDC4', '#FFD166', '#06D6A0', '#118AB2', '#7209B7'];
        const index = seed.split('').reduce((acc, char) => acc + char.charCodeAt(0), 0) % colors.length;
        return colors[index];
    };

    // --- ANALYZED FIX: renderIcon now ensures styles.avatar is applied ---
    const renderIcon = (item: Category) => {
        // Checking icon OR image as fallback
        const imageUri = item.icon;

        if (imageUri) {
            return (
                <View style={styles.avatar}>
                    <Image
                        source={{ uri: imageUri }}
                        style={styles.avatarImage}
                        resizeMode="cover"
                    />
                </View>
            );
        }

        const backgroundColor = item.color || getRandomColor(item.title);
        return (
            <View style={[styles.avatarFallback, { backgroundColor }]}>
                <Text style={styles.avatarLetter}>{getFirstLetter(item.title)}</Text>
            </View>
        );
    };

    const renderBadge = (type: string) => {
        const color = type === 'Product' ? '#0284C7' : type === 'Service' ? '#16A34A' : '#9333EA';
        const icon = type === 'Product' ? 'inventory' : type === 'Service' ? 'handyman' : 'widgets';
        return (
            <View style={[styles.badge, { backgroundColor: color + '20' }]}>
                <Icon name={icon} size={12} color={color} />
                <Text style={[styles.badgeText, { color }]}>{type}</Text>
            </View>
        );
    };

    const filteredCategories = categories.filter(category => {
        const matchesSearch = category.title.toLowerCase().includes(searchQuery.toLowerCase());
        const matchesType = !selectedType || category.type === selectedType;
        return matchesSearch && matchesType;
    });

    const renderItem = ({ item, index }: { item: Category; index: number }) => {
        return (
            <Animated.View style={[styles.card, { opacity: fadeAnim }]}>
                <TouchableOpacity style={styles.cardContent} activeOpacity={0.7} onPress={() => handleEdit(item)}>
                    <View style={styles.cardLeft}>
                        {/* ICON RENDERED HERE */}
                        {renderIcon(item)}

                        <View style={styles.cardInfo}>
                            <View style={styles.titleRow}>
                                <Text style={styles.title} numberOfLines={1}>{item.title}</Text>
                                {!item.isActive && (
                                    <View style={styles.inactiveBadge}>
                                        <Text style={styles.inactiveText}>Hidden</Text>
                                    </View>
                                )}
                            </View>

                            <View style={styles.badgeRow}>
                                {renderBadge(item.type)}
                                <Text style={styles.productCountText}>{item.productCount || 0} items</Text>
                            </View>
                        </View>
                    </View>

                    <View style={styles.actions}>
                        <TouchableOpacity
                            style={[styles.iconButton, styles.deleteButton]}
                            onPress={() => handleDelete(item._id, item.title)}
                        >
                            <Icon name="delete-outline" size={18} color="#EF4444" />
                        </TouchableOpacity>
                    </View>
                </TouchableOpacity>
            </Animated.View>
        );
    };

    const renderHeader = () => (
        <View style={styles.header}>
            <View style={styles.headerTop}>
                <View>
                    <Text style={styles.headerTitle}>Categories</Text>
                    <Text style={styles.headerSubtitle}>Manage your listings</Text>
                </View>
                <TouchableOpacity style={styles.addButton} onPress={() => router.push('/admin/category/add')}>
                    <Icon name="add" size={24} color={COLORS.white} />
                </TouchableOpacity>
            </View>

            <View style={styles.typeFilter}>
                {categoryTypes.map((type) => (
                    <TouchableOpacity
                        key={type.label}
                        onPress={() => setSelectedType(type.value)}
                        style={[
                            styles.typeButton,
                            selectedType === type.value && { backgroundColor: type.color, borderColor: type.color }
                        ]}
                    >
                        <Text style={[styles.typeButtonText, selectedType === type.value && { color: '#FFF' }]}>
                            {type.label}
                        </Text>
                    </TouchableOpacity>
                ))}
            </View>
        </View>
    );

    return (
        <View style={styles.container}>
            <FlatList
                data={filteredCategories}
                keyExtractor={(item) => item._id}
                renderItem={renderItem}
                ListHeaderComponent={renderHeader}
                contentContainerStyle={styles.list}
                refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} />}
                showsVerticalScrollIndicator={false}
            />
        </View>
    );
};

export default CategoryDisplayScreen;

const styles = StyleSheet.create({
    container: { flex: 1, backgroundColor: '#F8FAFC' },
    header: { padding: 20, backgroundColor: '#FFF', borderBottomLeftRadius: 24, borderBottomRightRadius: 24, ...SHADOWS.medium },
    headerTop: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginBottom: 20 },
    headerTitle: { ...FONTS.h1, color: COLORS.dark, fontWeight: '700' },
    headerSubtitle: { ...FONTS.body3, color: COLORS.gray },
    addButton: { width: 48, height: 48, borderRadius: 24, backgroundColor: COLORS.primary, justifyContent: 'center', alignItems: 'center' },
    typeFilter: { flexDirection: 'row', gap: 8 },
    typeButton: { flex: 1, paddingVertical: 8, borderRadius: 10, borderWidth: 1, borderColor: '#E2E8F0', alignItems: 'center' },
    typeButtonText: { ...FONTS.body4, fontWeight: '600', color: COLORS.dark },
    list: { paddingBottom: 30 },
    card: { backgroundColor: '#FFF', borderRadius: 20, marginHorizontal: 20, marginTop: 15, ...SHADOWS.small },
    cardContent: { flexDirection: 'row', padding: 15, alignItems: 'center' },
    cardLeft: { flex: 1, flexDirection: 'row', alignItems: 'center' },

    /* FIX: AVATAR STYLES */
    avatar: { width: 60, height: 60, borderRadius: 15, overflow: 'hidden', backgroundColor: '#F1F5F9' },
    avatarImage: { width: '100%', height: '100%' },
    avatarFallback: { width: 60, height: 60, borderRadius: 15, justifyContent: 'center', alignItems: 'center' },
    avatarLetter: { color: '#FFF', fontSize: 22, fontWeight: '700' },

    cardInfo: { flex: 1, marginLeft: 15 },
    titleRow: { flexDirection: 'row', alignItems: 'center', gap: 8 },
    title: { ...FONTS.h3, color: COLORS.dark, fontWeight: '600' },
    badgeRow: { flexDirection: 'row', alignItems: 'center', marginTop: 5, gap: 10 },
    badge: { flexDirection: 'row', alignItems: 'center', paddingHorizontal: 8, paddingVertical: 4, borderRadius: 6, gap: 4 },
    badgeText: { fontSize: 11, fontWeight: '700' },
    productCountText: { fontSize: 11, color: COLORS.gray },
    inactiveBadge: { backgroundColor: '#F3F4F6', paddingHorizontal: 6, borderRadius: 4 },
    inactiveText: { fontSize: 10, color: '#6B7280' },
    actions: { marginLeft: 10 },
    iconButton: { width: 36, height: 36, borderRadius: 10, justifyContent: 'center', alignItems: 'center' },
    deleteButton: { backgroundColor: '#FEF2F2' },
});