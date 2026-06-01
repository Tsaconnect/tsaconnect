// components/marketplace/header.tsx
//
// Marketplace search header — debounced product/service search with an
// absolutely-positioned results dropdown. Restyled onto the TSA Signature
// design system (theme tokens + @/components/ui primitives). Props, exports,
// data fetching (api.getMarketplaceProducts), state, the debounce, effects,
// and every navigation route + param are unchanged — visual / structural
// migration only.
import React, { useState, useEffect } from "react";
import { View, FlatList } from "react-native";
import { useDebounce } from "use-debounce";
import { router } from "expo-router";
import { api, Product } from "../services/api";
import { useTheme } from "@/theme";
import {
  Divider,
  Icon,
  Input,
  PressableScale,
  Spinner,
  Surface,
  Text,
} from "@/components/ui";

type HeaderSearchProps = {
  type: "Product" | "Service";
};

const HeaderSearch: React.FC<HeaderSearchProps> = ({ type }) => {
  const { colors, spacing, radius } = useTheme();
  const [search, setSearch] = useState<string>("");
  const [debouncedSearch] = useDebounce(search, 300);
  const [results, setResults] = useState<Product[]>([]);
  const [loading, setLoading] = useState<boolean>(false);
  const [error, setError] = useState<string | null>(null);
  const [isFocused, setIsFocused] = useState<boolean>(false);

  useEffect(() => {
    if (!debouncedSearch.trim()) {
      setResults([]);
      setError(null);
      setLoading(false);
      return;
    }
    let cancelled = false;
    setLoading(true);
    setError(null);

    (async () => {
      const response = await api.getMarketplaceProducts({
        search: debouncedSearch,
        type,
      });
      if (cancelled) return;
      if (response.success && Array.isArray(response.data?.products)) {
        setResults(response.data.products);
      } else {
        console.warn("Search failed:", response.message);
        setError("Failed to fetch search results");
        setResults([]);
      }
      setLoading(false);
    })();

    return () => {
      cancelled = true;
    };
  }, [debouncedSearch, type]);

  const handleSelect = (item: Product) => {
    const id = item.id || (item as any)._id;
    const image =
      item.images?.[0]?.url || ((item.images?.[0] as any) ?? "");
    if (type === "Service") {
      router.push({
        pathname: "/servicedetail",
        params: {
          id,
          title: item.name,
          description: item.description ?? "",
          image,
        },
      });
    } else {
      router.push({
        pathname: "/productdetails",
        params: {
          id,
          name: item.name,
          description: item.description ?? "",
          price: String(item.price ?? ""),
          image,
        },
      });
    }
    clearSearch();
  };

  const renderSearchResults = () => {
    if (!isFocused || debouncedSearch.trim() === "") return null;

    if (loading) {
      return (
        <Surface
          elevation="lg"
          radius="md"
          padding="md"
          style={styles.dropdown}
        >
          <View style={{ paddingVertical: spacing.lg, alignItems: "center" }}>
            <Spinner size="small" />
          </View>
        </Surface>
      );
    }

    if (error) {
      return (
        <Surface
          elevation="lg"
          radius="md"
          padding="md"
          style={styles.dropdown}
        >
          <View style={{ paddingVertical: spacing.lg, alignItems: "center" }}>
            <Text variant="bodySm" color="danger" align="center">
              {error}
            </Text>
          </View>
        </Surface>
      );
    }

    if (results.length === 0) {
      return (
        <Surface
          elevation="lg"
          radius="md"
          padding="md"
          style={styles.dropdown}
        >
          <View style={{ paddingVertical: spacing.lg, alignItems: "center" }}>
            <Text variant="bodySm" color="textMuted" align="center">
              No {type === "Service" ? "services" : "products"} match &quot;
              {debouncedSearch}&quot;
            </Text>
          </View>
        </Surface>
      );
    }

    return (
      <Surface
        elevation="lg"
        radius="md"
        style={[styles.dropdown, { maxHeight: 350, paddingVertical: spacing.xs }]}
      >
        <FlatList
          data={results}
          keyboardShouldPersistTaps="handled"
          keyExtractor={(item) => item.id || (item as any)._id}
          ItemSeparatorComponent={() => <Divider />}
          renderItem={({ item }) => (
            <PressableScale
              onPress={() => handleSelect(item)}
              style={{
                paddingHorizontal: spacing.md,
                paddingVertical: spacing.md,
              }}
            >
              <Text variant="bodySm" weight="600" numberOfLines={1}>
                {item.name}
              </Text>
              {item.companyName ? (
                <Text
                  variant="caption"
                  color="textMuted"
                  numberOfLines={1}
                  style={{ marginTop: 2 }}
                >
                  {item.companyName}
                </Text>
              ) : null}
            </PressableScale>
          )}
        />
      </Surface>
    );
  };

  const clearSearch = () => {
    setSearch("");
    setResults([]);
    setIsFocused(false);
  };

  return (
    <View
      style={{
        backgroundColor: colors.surface,
        paddingHorizontal: spacing.lg,
        paddingVertical: spacing.sm,
        borderBottomWidth: 1,
        borderBottomColor: colors.border,
        zIndex: 1,
      }}
    >
      <Input
        placeholder={`Search ${type === "Service" ? "services" : "products"}...`}
        value={search}
        onChangeText={setSearch}
        onFocus={() => setIsFocused(true)}
        onBlur={() => setIsFocused(false)}
        autoCorrect={false}
        returnKeyType="search"
        inputStyle={{ borderRadius: radius.pill }}
        leftAdornment={<Icon name="search" size={18} color={colors.textMuted} />}
        rightAdornment={
          search !== "" ? (
            <PressableScale onPress={clearSearch} hitSlop={8}>
              <Icon name="closeCircle" size={18} color={colors.textMuted} />
            </PressableScale>
          ) : undefined
        }
      />
      {renderSearchResults()}
    </View>
  );
};

export default HeaderSearch;

// Only the absolute-positioning geometry of the dropdown lives here; all colors,
// elevation, radius and spacing come from theme tokens / primitives above.
const styles = {
  dropdown: {
    position: "absolute" as const,
    top: "120%" as const,
    left: 10,
    right: 0,
    width: "98%" as const,
    zIndex: 1000,
  },
};
