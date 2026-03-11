import { StyleSheet, Text, View } from "react-native";
import React from "react";
import { useLocalSearchParams } from "expo-router";

const AdminDashboard = () => {
  const user = useLocalSearchParams();
  return (
    <View>
      <Text>Admin</Text>
    </View>
  );
};

export default AdminDashboard;

const styles = StyleSheet.create({});
