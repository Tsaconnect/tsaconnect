import { StyleSheet, View } from "react-native";
import React, { ReactNode } from "react";
import { SIZES } from "../../constants";

interface FeaturesProps {
  children: ReactNode;
}

const Features: React.FC<FeaturesProps> = ({ children }) => {
  return <View style={styles.content}>{children}</View>;
};

export default Features;

const styles = StyleSheet.create({
  content: {
    padding: SIZES.padding,
    backgroundColor: "#f5f5f5",
    borderRadius: 8,
  },
});
