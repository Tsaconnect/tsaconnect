import React, { useEffect, useState } from 'react';
import {
  ScrollView,
  View,
  Text,
  Image,
  StyleSheet,
  Pressable,
} from 'react-native';
import { COLORS } from '../../constants';
import { useAuth } from '../../AuthContext/AuthContext';
import { baseUrl } from '../../constants/api/apiClient';
import axios from 'axios';
import { router } from 'expo-router';
import LoadingSpinner from '../others/LoadingSpinner';

const ProductList = () => {
  const [categories, setCategories] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);
  const { token } = useAuth();

  useEffect(() => {
    const fetchCategories = async () => {
      try {
        const response = await axios.get(
          `${baseUrl}/category?isParent=true&isFeatured=true`,
          {
            headers: {
              Authorization: `${token}`,
            },
          }
        );

        const parentCategories = response.data.results;

        const categoriesWithChildren = await Promise.all(
          parentCategories.map(async (category) => {
            const childrenResponse = await axios.get(
              `${baseUrl}/category?parentCategory=${category.id}`,
              {
                headers: {
                  Authorization: `${token}`,
                },
              }
            );

            const limitedChildren = childrenResponse.data.results.slice(0, 4);
            return { ...category, children: limitedChildren };
          })
        );

        setCategories(categoriesWithChildren);
      } catch (error) {
        setError('Failed to load categories. Please try again later.');
      } finally {
        setLoading(false);
      }
    };

    fetchCategories();
  }, []);

  if (loading) {
    return (
      <View style={styles.loaderContainer}>
        <LoadingSpinner />
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

  return (
    <ScrollView style={styles.container}>
      {categories.map((category, index) => (
        <View key={index} style={styles.categoryContainer}>
          <Text style={styles.categoryTitle}>{category.title}</Text>
          {category.children.map((child, childIndex) => (
            <Pressable
              key={childIndex}
              style={styles.itemContainer}
              onPress={() =>
                router.push({
                  pathname: '/categoryproducts',
                  params: { value: child.id },
                })
              }
            >
              <Image
                source={{ uri: child.image }}
                style={styles.iconStyle}
                resizeMode="contain"
              />
              <Text style={styles.itemText}>{child.title}</Text>
            </Pressable>
          ))}
        </View>
      ))}
    </ScrollView>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    padding: 10,
    backgroundColor: '#fff',
  },
  loaderContainer: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
  },
  errorContainer: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    padding: 20,
  },
  errorText: {
    fontSize: 16,
    color: 'red',
    textAlign: 'center',
  },
  categoryContainer: {
    marginTop: 20,
  },
  categoryTitle: {
    fontSize: 16,
    fontWeight: '600',
    marginBottom: 10,
  },
  itemContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 5,
    borderColor: COLORS.gray,
    borderBottomWidth: 0.5,
    paddingVertical: 10,
  },
  iconStyle: {
    width: 50,
    height: 50,
    marginRight: 10,
  },
  itemText: {
    fontSize: 16,
  },
});

export default ProductList;
