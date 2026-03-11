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
import axios from "axios";
import { baseUrl } from "../../constants/api/apiClient";
import { useAuth } from "../../AuthContext/AuthContext";
import { useDebounce } from "use-debounce";
import { router } from "expo-router";

// Define types for the props and data items
type HeaderSearchProps = {
  type: string;
};

type SearchResultItem = {
  id: string;
  name: string;
};

const HeaderSearch: React.FC<HeaderSearchProps> = ({ type }) => {
  const { token } = useAuth();
  const [search, setSearch] = useState<string>("");
  const [debouncedSearch] = useDebounce(search, 300); // Debounced search input
  const [results, setResults] = useState<SearchResultItem[]>([]);
  const [loading, setLoading] = useState<boolean>(false);
  const [error, setError] = useState<string | null>(null);
  const [isFocused, setIsFocused] = useState<boolean>(false);

  // Fetch search results based on debounced input
  useEffect(() => {
    const fetchSearchResults = async (query: string) => {
      setLoading(true);
      setError(null);
      try {
        const response = await axios.get<SearchResultItem[]>(`${baseUrl}/search`, {
          headers: {
            Authorization: `${token}`,
          },
          params: {
            name: query,
            type,
          },
        });
        setResults(response.data);
      } catch (err) {
        setError("Failed to fetch search results");
        console.error("Error fetching search results:", err);
      } finally {
        setLoading(false);
      }
    };

    if (debouncedSearch.trim()) {
      fetchSearchResults(debouncedSearch);
    } else {
      setResults([]);
    }
  }, [debouncedSearch, token, type]);

  const renderSearchResults = () => {
    if (loading) {
      return (
        <View style={styles.loadingContainer}>
          <ActivityIndicator size="large" color={COLORS.primary} />
        </View>
      );
    }

    if (error) {
      return (
        <View style={styles.errorContainer}>
          <Text style={styles.errorText}>{error}</Text>
        </View>
      );
    }

    if (isFocused && debouncedSearch.trim() !== "" && results.length > 0) {
      return (
        <View style={styles.dropdown}>
          <FlatList
            data={results}
            keyExtractor={(item) => item.id}
            renderItem={({ item }) => (
              <TouchableOpacity
                style={styles.dropdownItem}
                onPress={() => {
                  router.push({
                    pathname: "/productdetails",
                    params: item,
                  });
                }}
              >
                <Text>{item.name}</Text>
              </TouchableOpacity>
            )}
          />
        </View>
      );
    }

    return null;
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
          placeholder="Search..."
          value={search}
          onChangeText={setSearch}
          onFocus={() => setIsFocused(true)}
          onBlur={() => setIsFocused(false)}
          style={styles.searchInput}
        />
        {search !== "" && (
          <TouchableOpacity onPress={clearSearch}>
            <Ionicons name="close-circle" size={20} color="#666" style={styles.clearIcon} />
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
  loadingContainer: {
    alignItems: "center",
    marginTop: 10,
  },
  errorContainer: {
    alignItems: "center",
    marginTop: 10,
  },
  errorText: {
    color: "red",
    textAlign: "center",
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
    paddingHorizontal: 15,
    paddingVertical: 8,
    width: "98%",
  },
  dropdownItem: {
    padding: 10,
    borderBottomColor: COLORS.gray,
    borderBottomWidth: 1,
  },
});
