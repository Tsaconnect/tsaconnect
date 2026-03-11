import { StyleSheet, Text, TouchableOpacity, View } from "react-native";
import React from "react";
import { SIZES } from "../../constants";

const LeftTab = () => {
  return (
    <View style={styles.buttonContainer}>
      <TouchableOpacity>
        <Text>Deposit</Text>
      </TouchableOpacity>
    </View>
  );
};

export default LeftTab;

const styles = StyleSheet.create({
  buttonContainer: {
    height: SIZES.height * 0.05901,
    backgroundColor: "#F5FCFF",
    alignItems: "center",
    justifyContent: "center",
    width: SIZES.width * 0.4534883,
    borderWidth: 2,
    borderColor: "#F5FCFF",
    elevation: 10, // Only for Android (shadow effect)
    shadowColor: "#000", // Shadow color for iOS
    shadowOffset: { width: 0, height: 2 }, // Shadow position for iOS
    shadowOpacity: 0.25, // Shadow opacity for iOS
    shadowRadius: 3.84, // Shadow blur for iOS
  },
});
