import { StyleSheet, View } from "react-native";
import React, { useEffect, useRef, useState } from "react";
import { COLORS, SIZES } from "../../../../constants";
import { useAuth } from "../../../../AuthContext/AuthContext";
import axios from "axios";
import { baseUrl } from "../../../../constants/api/apiClient";
import TradeAndEarnScreen from "@/screens/TradeAndEarnScreen";

const services = () => {
  const [categories, setCategories] = useState([]);
  const { token } = useAuth();
  useEffect(() => {
    const fetchCategories = async () => {
      try {
        const response = await axios.get(`${baseUrl}/category?type=Service`, {
          headers: {
            Authorization: `${token}`,
          },
        });
        const fetchedCategories = response.data.results;
        setCategories(fetchedCategories);
      } catch (error) {
        alert(error?.response?.data?.message);
      }
    };
    if (token) {
      fetchCategories();
    }
  }, []);
  return (
    <View style={{ flex: 1, backgroundColor: COLORS.white }}>
     
      <TradeAndEarnScreen/>
    </View>
  );
};

export default services;

export const styles = StyleSheet.create({});
