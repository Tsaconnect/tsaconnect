import React from "react";
import { View, Text, StyleSheet, TouchableOpacity } from "react-native";
import Icon from "@expo/vector-icons/FontAwesome";
import { COLORS } from "../../constants"; // Adjust the path according to your project structure
import { router } from "expo-router";

const PaymentReceivedScreen = () => {
  return (
    <View style={styles.container}>
      <View style={styles.header}>
        <TouchableOpacity onPress={() => router.back()}>
          <Text style={styles.doneButton}>Done</Text>
        </TouchableOpacity>
      </View>
      <View style={styles.content}>
        <View style={styles.iconContainer}>
          <Icon name="check-circle" size={80} color="green" />
        </View>
        <Text style={styles.messageText}>Payment Received successful</Text>
      </View>
    </View>
  );
};

export default PaymentReceivedScreen;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: "white",
    paddingHorizontal: 20,
  },
  header: {
    flexDirection: "row",
    justifyContent: "flex-end",
    paddingVertical: 15,
  },
  doneButton: {
    fontSize: 16,
    color: "orange",
    fontWeight: "bold",
  },
  content: {
    flex: 1,
    justifyContent: "center",
    alignItems: "center",
  },
  iconContainer: {
    marginBottom: 20,
  },
  messageText: {
    fontSize: 18,
    fontWeight: "bold",
    color: "black",
  },
});
