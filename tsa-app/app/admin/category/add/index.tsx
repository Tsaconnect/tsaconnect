import React, { useEffect, useState, useRef } from "react";
import {
    Alert,
    Image,
    ScrollView,
    StyleSheet,
    Text,
    TextInput,
    TouchableOpacity,
    View,
    Animated,
    KeyboardAvoidingView,
    Platform,
} from "react-native";
import { COLORS, SIZES, SHADOWS, FONTS } from "@/constants/theme";
import Icon from "@expo/vector-icons/FontAwesome";
import * as ImagePicker from "expo-image-picker";
import { Picker } from "@react-native-picker/picker";
import { router } from "expo-router";
import LoadingSpinner from "@/components/others/LoadingSpinner";
import api, { Category } from "@/components/services/api";

// --- SUB-COMPONENTS MOVED OUTSIDE TO PREVENT RE-MOUNTING ON EVERY RENDER ---

const Checkbox = ({
    checked,
    onPress,
    label,
}: {
    checked: boolean;
    onPress: () => void;
    label: string;
}) => (
    <TouchableOpacity style={styles.checkboxContainer} onPress={onPress} activeOpacity={0.7}>
        <View style={[styles.checkbox, checked && styles.checkboxChecked]}>
            {checked && <Icon name="check" size={16} color={COLORS.white} />}
        </View>
        <Text style={styles.checkboxLabel}>{label}</Text>
    </TouchableOpacity>
);

const InputWithIcon = ({ icon, label, children }: { icon: string; label: string; children: React.ReactNode }) => (
    <View style={styles.inputWithIconContainer}>
        <View style={styles.inputIconHeader}>
            <Icon
                //@ts-ignore
                name={icon} size={16} color={COLORS.primary} />
            <Text style={styles.inputIconLabel}>{label}</Text>
        </View>
        {children}
    </View>
);

// --- MAIN SCREEN COMPONENT ---

const AddCategoryScreen = () => {
    const [formData, setFormData] = useState({
        title: "",
        description: "",
        type: "" as "Product" | "Service" | "Both" | "",
        parentCategory: "",
        isActive: true,
        order: 0,
    });
    const [featuredImage, setFeaturedImage] = useState<string | null>(null);
    const [hasParent, setHasParent] = useState(false);
    const [parentCategories, setParentCategories] = useState<Category[]>([]);
    const [loading, setLoading] = useState(false);
    const [imageLoading, setImageLoading] = useState(false);
    const [categoriesLoading, setCategoriesLoading] = useState(false);
    const [isAuthenticated, setIsAuthenticated] = useState(false);
    const [activeInput, setActiveInput] = useState<string | null>(null);

    const fadeAnim = useRef(new Animated.Value(0)).current;
    const slideAnim = useRef(new Animated.Value(30)).current;

    useEffect(() => {
        checkAuthentication();

        Animated.parallel([
            Animated.timing(fadeAnim, {
                toValue: 1,
                duration: 500,
                useNativeDriver: true,
            }),
            Animated.timing(slideAnim, {
                toValue: 0,
                duration: 500,
                useNativeDriver: true,
            }),
        ]).start();
    }, []);

    useEffect(() => {
        if (isAuthenticated && formData.type) {
            fetchParentCategories();
        }
    }, [isAuthenticated, formData.type]);

    const checkAuthentication = async () => {
        try {
            const authenticated = await api.checkAuth();
            setIsAuthenticated(authenticated);
            if (!authenticated) router.push("/login");
        } catch (error) {
            console.error("Auth check error:", error);
            router.push("/login");
        }
    };

    const fetchParentCategories = async () => {
        try {
            setCategoriesLoading(true);
            const response = await api.getParentCategories(formData.type === "" ? undefined : formData.type);
            if (response.success && response.data) {
                setParentCategories(response.data);
            }
        } catch (error) {
            console.error("Error fetching parent categories:", error);
        } finally {
            setCategoriesLoading(false);
        }
    };

    const pickImageAsync = async () => {
        try {
            setImageLoading(true);
            const { status } = await ImagePicker.requestMediaLibraryPermissionsAsync();
            if (status !== "granted") {
                Alert.alert("Permission Required", "Please grant access to your photo library");
                return;
            }

            const result = await ImagePicker.launchImageLibraryAsync({
                mediaTypes: ImagePicker.MediaTypeOptions.Images,
                allowsEditing: true,
                aspect: [16, 9],
                quality: 0.8,
            });

            if (!result.canceled && result.assets[0]) {
                setFeaturedImage(result.assets[0].uri);
            }
        } finally {
            setImageLoading(false);
        }
    };

    const handleInputChange = (field: string, value: any) => {
        setFormData((prev) => ({ ...prev, [field]: value }));
        if (field === "type") {
            setFormData(prev => ({ ...prev, parentCategory: "" }));
            setHasParent(false);
        }
    };

    const createCategory = async () => {
        if (!formData.title.trim() || !formData.type) {
            Alert.alert("Validation Error", "Title and Type are required");
            return;
        }

        try {
            setLoading(true);
            const formDataToSend = new FormData();

            if (featuredImage) {
                const filename = featuredImage.split('/').pop() || `cat_${Date.now()}.jpg`;
                const match = /\.(\w+)$/.exec(filename);
                const type = match ? `image/${match[1]}` : 'image/jpeg';
                formDataToSend.append('image', { uri: featuredImage, name: filename, type } as any);
            }

            formDataToSend.append('title', formData.title);
            formDataToSend.append('description', formData.description);
            formDataToSend.append('type', formData.type);
            formDataToSend.append('isActive', String(formData.isActive));
            formDataToSend.append('order', '0');
            formDataToSend.append('color', '#4A90E2');

            if (hasParent && formData.parentCategory) {
                formDataToSend.append('parentCategory', formData.parentCategory);
            }

            const response = await api.createCategory(formDataToSend);

            if (response.success) {
                Alert.alert("🎉 Success", "Category created", [{ text: "OK", onPress: () => router.back() }]);
                //@ts-ignore
                setFormData({ title: "", description: "", type: "", isActive: true, parentCategory: "" });
                setFeaturedImage(null);
                setHasParent(false);
                return;
            }
        } catch (error) {
            Alert.alert("Error", "Failed to create category");
        } finally {
            setLoading(false);
        }
    };

    if (!isAuthenticated) {
        return (
            <View style={styles.loadingContainer}>
                <LoadingSpinner />
                <Text style={styles.loadingText}>Checking authentication...</Text>
            </View>
        );
    }

    return (
        <KeyboardAvoidingView
            behavior={Platform.OS === "ios" ? "padding" : "height"}
            style={styles.container}
        >
            <ScrollView
                style={styles.scrollView}
                showsVerticalScrollIndicator={false}
                contentContainerStyle={styles.scrollContent}
                keyboardShouldPersistTaps="handled"
            >
                <Animated.View style={[styles.header, { opacity: fadeAnim, transform: [{ translateY: slideAnim }] }]}>
                    <TouchableOpacity style={styles.backButton} onPress={() => router.back()}>
                        <Icon name="arrow-left" size={20} color={COLORS.dark} />
                    </TouchableOpacity>
                    <View style={styles.headerContent}>
                        <Text style={styles.headerTitle}>Add New Category</Text>
                        <Text style={styles.headerSubtitle}>Fill in the details below</Text>
                    </View>
                </Animated.View>

                <Animated.View style={[styles.formCard, { opacity: fadeAnim, transform: [{ translateY: slideAnim }] }]}>

                    <InputWithIcon icon="header" label="Category Title">
                        <TextInput
                            style={[styles.input, activeInput === 'title' && styles.inputFocused]}
                            value={formData.title}
                            onChangeText={(value) => handleInputChange("title", value)}
                            onFocus={() => setActiveInput('title')}
                            onBlur={() => setActiveInput(null)}
                            placeholder="e.g., Electronics"
                            placeholderTextColor={COLORS.gray}
                        />
                    </InputWithIcon>

                    <InputWithIcon icon="align-left" label="Description">
                        <TextInput
                            style={[styles.input, styles.textArea, activeInput === 'description' && styles.inputFocused]}
                            value={formData.description}
                            onChangeText={(value) => handleInputChange("description", value)}
                            onFocus={() => setActiveInput('description')}
                            onBlur={() => setActiveInput(null)}
                            placeholder="Describe your category..."
                            multiline
                            numberOfLines={4}
                        />
                    </InputWithIcon>

                    <InputWithIcon icon="tag" label="Category Type">
                        <View style={styles.pickerContainer}>
                            <Picker
                                selectedValue={formData.type}
                                onValueChange={(value) => handleInputChange("type", value)}
                            >
                                <Picker.Item label="Select Type" value="" />
                                <Picker.Item label="📦 Product" value="Product" />
                                <Picker.Item label="🛠️ Service" value="Service" />
                            </Picker>
                        </View>
                    </InputWithIcon>

                    <InputWithIcon icon="image" label="Featured Image">
                        <TouchableOpacity style={styles.uploadContainer} onPress={pickImageAsync}>
                            {featuredImage ? (
                                <Image source={{ uri: featuredImage }} style={styles.uploadedImage} />
                            ) : (
                                <Text style={styles.uploadText}>Upload Image</Text>
                            )}
                        </TouchableOpacity>
                    </InputWithIcon>

                    <Checkbox
                        checked={hasParent}
                        onPress={() => setHasParent(!hasParent)}
                        label="Set Parent Category"
                    />

                    {hasParent && (
                        <InputWithIcon icon="sitemap" label="Parent Category">
                            <View style={styles.pickerContainer}>
                                <Picker
                                    selectedValue={formData.parentCategory}
                                    onValueChange={(value) => handleInputChange("parentCategory", value)}
                                >
                                    <Picker.Item label="Select Parent" value="" />
                                    {parentCategories.map((item) => (
                                        <Picker.Item key={item._id} label={item.title} value={item._id} />
                                    ))}
                                </Picker>
                            </View>
                        </InputWithIcon>
                    )}

                    <View style={styles.buttonGroup}>
                        <TouchableOpacity
                            style={[styles.button, loading && styles.buttonDisabled]}
                            onPress={createCategory}
                            disabled={loading}
                        >
                            {loading ? <LoadingSpinner /> : <Text style={styles.buttonText}>Create Category</Text>}
                        </TouchableOpacity>
                    </View>

                </Animated.View>
            </ScrollView>
        </KeyboardAvoidingView>
    );
};

export default AddCategoryScreen;

// Keep your existing styles below
const styles = StyleSheet.create({
    container: { flex: 1, backgroundColor: COLORS.lightPrimary },
    loadingContainer: { flex: 1, justifyContent: "center", alignItems: "center" },
    loadingText: { ...FONTS.body3, color: COLORS.gray, marginTop: 10 },
    scrollView: { flex: 1 },
    scrollContent: { paddingBottom: 40 },
    header: {
        flexDirection: "row",
        alignItems: "center",
        paddingHorizontal: SIZES.padding,
        paddingTop: SIZES.padding * 2,
        paddingBottom: SIZES.padding * 1.5,
        backgroundColor: COLORS.white,
        borderBottomLeftRadius: 20,
        borderBottomRightRadius: 20,
        ...SHADOWS.medium,
    },
    backButton: {
        padding: 10, marginRight: 10, borderRadius: 10, backgroundColor: COLORS.lightGray
    },
    headerContent: { flex: 1 },
    headerTitle: { ...FONTS.h3, color: COLORS.dark, fontWeight: "700" },
    headerSubtitle: { ...FONTS.body3, color: COLORS.gray },
    formCard: { margin: SIZES.padding, padding: SIZES.padding * 1.5, backgroundColor: COLORS.white, borderRadius: 20, ...SHADOWS.medium },
    inputWithIconContainer: { marginBottom: SIZES.padding * 1.5 },
    inputIconHeader: { flexDirection: "row", alignItems: "center", marginBottom: 8 },
    inputIconLabel: { ...FONTS.body4, fontWeight: "600", color: COLORS.dark, marginLeft: 8 },
    input: { backgroundColor: COLORS.lightGray, borderRadius: 12, padding: SIZES.padding, ...FONTS.body4, color: COLORS.dark, borderWidth: 2, borderColor: 'transparent' },
    inputFocused: { borderColor: COLORS.primary, backgroundColor: `${COLORS.primary}08` },
    textArea: { height: 100, textAlignVertical: "top" },
    pickerContainer: { backgroundColor: COLORS.lightGray, borderRadius: 12, overflow: "hidden" },
    uploadContainer: { backgroundColor: COLORS.lightGray, borderRadius: 12, height: 150, justifyContent: "center", alignItems: "center", overflow: "hidden" },
    uploadedImage: { width: "100%", height: "100%" },
    uploadText: { color: COLORS.primary, fontWeight: "600" },
    checkboxContainer: { flexDirection: "row", alignItems: "center", marginVertical: 10 },
    checkbox: { width: 24, height: 24, borderRadius: 6, borderWidth: 2, borderColor: COLORS.gray, marginRight: 10, justifyContent: 'center', alignItems: 'center' },
    checkboxChecked: { backgroundColor: COLORS.primary, borderColor: COLORS.primary },
    checkboxLabel: { ...FONTS.body4, color: COLORS.dark },
    buttonGroup: { marginTop: 20 },
    button: { backgroundColor: COLORS.primary, borderRadius: 14, height: 58, alignItems: "center", justifyContent: "center", ...SHADOWS.medium },
    buttonDisabled: { opacity: 0.6 },
    buttonText: { ...FONTS.h4, color: COLORS.white, fontWeight: "600" },
});