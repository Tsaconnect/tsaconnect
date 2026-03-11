import { View, StyleSheet } from "react-native";
import React from "react";
import Dots from "./Dots";
import { COLORS } from "../../constants/theme";

interface ProgressOneProps {
  index: number;
}

export const ProgressOne: React.FC<ProgressOneProps> = ({ index }) => {
  return (
    <View style={{ flexDirection: "row", justifyContent: "center" }}>
      {index === 1 && (
        <>
          <View style={styles.activeDot}></View>
          <Dots />
          <Dots />
        </>
      )}
      {index === 2 && (
        <>
          <Dots />
          <View style={styles.activeDot}></View>
          <Dots />
        </>
      )}
      {index === 3 && (
        <>
          <Dots />
          <Dots />
          <View style={styles.activeDot}></View>
        </>
      )}
    </View>
  );
};

export const ProgressTwo: React.FC = () => {
  return (
    <View style={styles.progressContainer}>
      <Dots />
      <View style={styles.largeActiveDot}></View>
      <Dots />
    </View>
  );
};

export const ProgressThree: React.FC = () => {
  return (
    <View style={styles.progressContainer}>
      <Dots />
      <Dots />
      <View style={styles.largeActiveDot}></View>
      <Dots />
    </View>
  );
};

export const ProgressFour: React.FC = () => {
  return (
    <View style={styles.progressContainer}>
      <Dots />
      <Dots />
      <Dots />
      <View style={styles.largeActiveDot}></View>
    </View>
  );
};

const styles = StyleSheet.create({
  activeDot: {
    backgroundColor: COLORS.primary,
    width: 10,
    height: 10,
    borderRadius: 25,
    margin: 5,
  },
  largeActiveDot: {
    backgroundColor: COLORS.primary,
    width: 14,
    height: 14,
    borderRadius: 25,
    margin: 5,
    marginTop: 3,
  },
  progressContainer: {
    flexDirection: "row",
    justifyContent: "center",
    flex: 1,
  },
});
