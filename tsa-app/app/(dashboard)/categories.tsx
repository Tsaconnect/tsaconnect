// app/(dashboard)/categories.tsx
//
// Categories browse screen — search, list, "view all" paging, and an add-category
// CTA. Restyled onto the TSA Signature design system (theme tokens +
// @/components/ui primitives). All data fetching (api.getStoredToken/setToken,
// api.getProfile, api.getCategories, api.deleteCategory), state, effects, the
// search filter, role fetch, showAll paging, and navigation routes/params are
// preserved verbatim — this is a visual/structural migration only.
import React, { useState, useEffect } from "react";
import { View, Image, FlatList, Alert } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { router } from "expo-router";
import api from "@/components/services/api";
import { useTheme } from "@/theme";
import {
  Button,
  Card,
  EmptyState,
  Icon,
  Input,
  PressableScale,
  Spinner,
  Text,
} from "@/components/ui";

const Categories = () => {
  const { colors, spacing, radius } = useTheme();
  const [categories, setCategories] = useState<any[]>([]);
  const [loading, setLoading] = useState(true);
  const [showAll, setShowAll] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const [filteredCategories, setFilteredCategories] = useState<any[]>([]);
  const [userRole, setUserRole] = useState<string | null>(null);

  const initialize = async () => {
    try {
      // Ensure api has token loaded
      const token = await api.getStoredToken();
      if (token) {
        api.setToken(token);
      }

      // Fetch user profile for role check
      const profileRes = await api.getProfile();
      if (profileRes.success && profileRes.data) {
        setUserRole(profileRes.data.role);
      }

      // Fetch categories
      const response = await api.getCategories({ active: true });
      if (response.success && response.data) {
        const list = Array.isArray(response.data) ? response.data : [];
        setCategories(list);
        setFilteredCategories(list);
      } else {
        setCategories([]);
        setFilteredCategories([]);
      }
    } catch (error: any) {
      console.error("Error loading categories:", error);
      setCategories([]);
      setFilteredCategories([]);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    initialize();
  }, []);

  useEffect(() => {
    setFilteredCategories(
      categories.filter((category) =>
        category.title?.toLowerCase().includes(searchQuery.toLowerCase())
      )
    );
  }, [searchQuery, categories]);

  const handleDelete = async (id: any) => {
    try {
      setLoading(true);
      const response = await api.deleteCategory(id);
      if (response.success) {
        setCategories(categories.filter((category) => category.id !== id));
        Alert.alert("Success", "The category was deleted successfully.");
      } else {
        Alert.alert("Error", response.message || "Error deleting category");
      }
    } catch (error) {
      console.error("Error deleting category:", error);
      Alert.alert("Error", "Error Deleting Category");
    } finally {
      setLoading(false);
    }
  };

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={["top"]}>
      <View style={{ flex: 1, padding: spacing.lg }}>
        <View
          style={{
            flexDirection: "row",
            alignItems: "center",
            gap: spacing.sm,
            marginBottom: spacing.lg,
          }}
        >
          <Input
            containerStyle={{ flex: 1 }}
            placeholder="Search Categories"
            value={searchQuery}
            onChangeText={(text) => setSearchQuery(text)}
            returnKeyType="search"
            leftAdornment={<Icon name="search" size={18} color={colors.textMuted} />}
            rightAdornment={
              searchQuery.length > 0 ? (
                <PressableScale onPress={() => setSearchQuery("")} hitSlop={8}>
                  <Icon name="close" size={16} color={colors.textMuted} />
                </PressableScale>
              ) : undefined
            }
          />
          <Button
            title="Add Category"
            size="md"
            fullWidth={false}
            leftIcon="plus"
            onPress={() => router.push("/add-category")}
          />
        </View>

        {loading ? (
          <View style={{ flex: 1, alignItems: "center", justifyContent: "center" }}>
            <Spinner size="large" />
          </View>
        ) : (
          <FlatList
            data={showAll ? filteredCategories : filteredCategories.slice(0, 20)}
            keyExtractor={(item) => item.id?.toString() || item._id}
            showsVerticalScrollIndicator={false}
            contentContainerStyle={{ paddingBottom: spacing["4xl"], gap: spacing.md }}
            renderItem={({ item }) => (
              <Card padding="md">
                <View style={{ flexDirection: "row", alignItems: "center", gap: spacing.md }}>
                  {item.image ? (
                    <Image
                      source={{ uri: item.image }}
                      style={{
                        width: 48,
                        height: 48,
                        borderRadius: radius.md,
                        backgroundColor: colors.surfaceAlt,
                      }}
                      resizeMode="cover"
                    />
                  ) : (
                    <View
                      style={{
                        width: 48,
                        height: 48,
                        borderRadius: radius.md,
                        alignItems: "center",
                        justifyContent: "center",
                        backgroundColor: colors.accentMuted,
                      }}
                    >
                      <Icon name="list" size={22} color={colors.accent} />
                    </View>
                  )}
                  <Text variant="bodyMed" weight="700" numberOfLines={1} style={{ flex: 1 }}>
                    {item.title}
                  </Text>
                </View>
              </Card>
            )}
            ListEmptyComponent={
              <EmptyState
                icon="list"
                title="No categories found"
                subtitle="Try a different search term."
              />
            }
            ListFooterComponent={
              !showAll && filteredCategories.length > 20 ? (
                <Button
                  title="View All"
                  variant="secondary"
                  onPress={() => setShowAll(true)}
                  style={{ marginTop: spacing.md }}
                />
              ) : null
            }
          />
        )}
      </View>
    </SafeAreaView>
  );
};

export default Categories;
