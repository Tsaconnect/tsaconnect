import React, { useEffect, useState } from "react";
import { StyleSheet, View } from "react-native";
import { COLORS } from "../../../../constants";
import HeaderSearch from "../../../../components/marketplace/header";
import { useAuth } from "../../../../AuthContext/AuthContext";
import axios from "axios";
import { baseUrl } from "../../../../constants/api/apiClient";
import ProductListCard from "../../../../components/accessories/ProductListCard";

const Products = () => {
  const [categories, setCategories] = useState([]);
  const { token } = useAuth();

  useEffect(() => {
    const fetchCategories = async () => {
      try {
        const response = await axios.get(`${baseUrl}/category?type=Product`, {
          headers: {
            Authorization: `${token}`,
          },
        });
        const fetchedCategories = response.data.results;
        setCategories(fetchedCategories);
      } catch (error) {
        alert(error?.response.data.message);
        if(error?.response.data.message==='Please authenticate'){
          
        }
      }
    };
    if (token) {
      fetchCategories();
    }
  }, []);

  return (
    <View style={styles.container}>
      <HeaderSearch type='Product'/>
      <ProductListCard itemList={categories} itemValue="categoryproducts" />
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: COLORS.white,
  },
});

export default Products;
