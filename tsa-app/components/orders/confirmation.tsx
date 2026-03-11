import React from "react";
import {
  View,
  Text,
  StyleSheet,
  ActivityIndicator,
  TouchableOpacity,
} from "react-native";
import Icon from "@expo/vector-icons/FontAwesome";
import { COLORS } from "../../constants"; // Adjust the path according to your project structure
import { router } from "expo-router";

const ConfirmationScreen = () => {
  return (
    <View style={styles.container}>
      <View style={styles.content}>
        <View style={styles.messageContainer}>
          <Icon name="info-circle" size={24} color={COLORS.primary} />
          <Text style={styles.messageText}>
            Hang Tight, confirmation of payment in progress
          </Text>
        </View>
        <ActivityIndicator
          size="large"
          color={COLORS.primary}
          style={styles.spinner}
        />
        <TouchableOpacity
          onPress={() => {
            router.push("/products");
          }}
          style={styles.back}
        >
          <Text style={{ padding: 10, color: COLORS.white }}>
            Return to dashboard
          </Text>
        </TouchableOpacity>
      </View>
    </View>
  );
};

export default ConfirmationScreen;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: "white",
    justifyContent: "center",
    alignContent: "center",
    alignItems: "center",
  },
  header: {
    flexDirection: "row",
    alignItems: "center",
    paddingVertical: 15,
    position: "absolute",
    top: 10,
  },
  headerTitle: {
    fontSize: 18,
    fontWeight: "bold",
    marginLeft: 10,
  },
  content: {
    justifyContent: "center",
    alignItems: "center",
    position: "absolute",
    top: 10,
  },
  messageContainer: {
    flexDirection: "row",
    alignItems: "center",
    backgroundColor: "#FFF5E5", // Adjust the background color to match the image
    padding: 15,
    borderRadius: 10,
    marginBottom: 200,
  },
  messageText: {
    marginLeft: 10,
    fontSize: 14,
    color: "black",
  },
  spinner: {
    marginTop: 20,
  },
  back: {
    marginTop: 20,
    borderRadius: 10,
    backgroundColor: COLORS.primary,
    alignItems: "center",
  },
});
