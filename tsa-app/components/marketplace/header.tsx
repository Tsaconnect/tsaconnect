import React, { useState, useEffect } from "react";
import {
  StyleSheet,
  TextInput,
  View,
  FlatList,
  Text,
  ActivityIndicator,
  TouchableOpacity,
} from "react-native";
import Ionicons from "@expo/vector-icons/Ionicons";
import { SIZES, COLORS } from "../../constants";
import { useDebounce } from "use-debounce";
import { router } from "expo-router";
import { api, Product } from "../services/api";

type HeaderSearchProps = {
  type: "Product" | "Service";
};

const HeaderSearch: React.FC<HeaderSearchProps> = ({ type }) => {
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
        <View style={styles.dropdown}>
          <View style={styles.statusContainer}>
            <ActivityIndicator size="small" color={COLORS.primary} />
          </View>
        </View>
      );
    }

    if (error) {
      return (
        <View style={styles.dropdown}>
          <View style={styles.statusContainer}>
            <Text style={styles.errorText}>{error}</Text>
          </View>
        </View>
      );
    }

    if (results.length === 0) {
      return (
        <View style={styles.dropdown}>
          <View style={styles.statusContainer}>
            <Text style={styles.emptyText}>
              No {type === "Service" ? "services" : "products"} match "{debouncedSearch}"
            </Text>
          </View>
        </View>
      );
    }

    return (
      <View style={styles.dropdown}>
        <FlatList
          data={results}
          keyboardShouldPersistTaps="handled"
          keyExtractor={(item) => item.id || (item as any)._id}
          renderItem={({ item }) => (
            <TouchableOpacity
              style={styles.dropdownItem}
              onPress={() => handleSelect(item)}
            >
              <Text style={styles.dropdownItemTitle} numberOfLines={1}>
                {item.name}
              </Text>
              {item.companyName ? (
                <Text style={styles.dropdownItemSub} numberOfLines={1}>
                  {item.companyName}
                </Text>
              ) : null}
            </TouchableOpacity>
          )}
        />
      </View>
    );
  };

  const clearSearch = () => {
    setSearch("");
    setResults([]);
    setIsFocused(false);
  };

  return (
    <View style={styles.container}>
      <View style={styles.searchBar}>
        <Ionicons name="search" size={20} color="#666" style={styles.searchIcon} />
        <TextInput
          placeholder={`Search ${type === "Service" ? "services" : "products"}...`}
          value={search}
          onChangeText={setSearch}
          onFocus={() => setIsFocused(true)}
          onBlur={() => setIsFocused(false)}
          style={styles.searchInput}
          autoCorrect={false}
          returnKeyType="search"
        />
        {search !== "" && (
          <TouchableOpacity onPress={clearSearch}>
            <Ionicons
              name="close-circle"
              size={20}
              color="#666"
              style={styles.clearIcon}
            />
          </TouchableOpacity>
        )}
      </View>
      {renderSearchResults()}
    </View>
  );
};

export default HeaderSearch;

const styles = StyleSheet.create({
  container: {
    backgroundColor: "#fff",
    paddingHorizontal: SIZES.padding,
    paddingVertical: 10,
    borderBottomWidth: 1,
    borderBottomColor: "#ddd",
    zIndex: 1,
  },
  searchBar: {
    flexDirection: "row",
    alignItems: "center",
    backgroundColor: "#f0f0f0",
    borderRadius: 20,
    paddingHorizontal: 15,
    paddingVertical: 8,
  },
  searchIcon: {
    marginRight: 10,
  },
  searchInput: {
    flex: 1,
    fontSize: 16,
    color: "#333",
  },
  clearIcon: {
    marginLeft: 10,
  },
  statusContainer: {
    paddingVertical: 16,
    alignItems: "center",
  },
  errorText: {
    color: "#DC2626",
    textAlign: "center",
  },
  emptyText: {
    color: "#888",
    textAlign: "center",
    fontSize: 14,
  },
  dropdown: {
    position: "absolute",
    top: "130%",
    left: 10,
    right: 0,
    backgroundColor: "#fff",
    maxHeight: 350,
    borderRadius: 10,
    shadowColor: "#000",
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.1,
    shadowRadius: 8,
    elevation: 5,
    zIndex: 1000,
    paddingHorizontal: 8,
    paddingVertical: 4,
    width: "98%",
  },
  dropdownItem: {
    paddingHorizontal: 12,
    paddingVertical: 12,
    borderBottomColor: COLORS.gray,
    borderBottomWidth: StyleSheet.hairlineWidth,
  },
  dropdownItemTitle: {
    fontSize: 14,
    color: "#1A1A1A",
    fontWeight: "600",
  },
  dropdownItemSub: {
    fontSize: 12,
    color: "#888",
    marginTop: 2,
  },
});
