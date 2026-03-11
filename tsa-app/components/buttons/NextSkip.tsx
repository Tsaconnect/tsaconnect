import { View, Text, TouchableOpacity, StyleSheet } from "react-native";
import React from "react";
import {
  ProgressFour,
  ProgressOne,
  ProgressThree,
  ProgressTwo,
} from "./Progress";
import { COLORS, SIZES } from "../../constants/theme";

interface ButtonProps {
  handleNext: () => void;
  handleSkip: () => void;
}

interface SkipNextButtonProps extends ButtonProps {
  index: number;
}

export const NextBackOne: React.FC<ButtonProps> = ({
  handleNext,
  handleSkip,
}) => {
  return (
    <View>
      <View style={styles.button}>
        <TouchableOpacity onPress={handleNext}>
          <Text>Next</Text>
        </TouchableOpacity>
      </View>
      <TouchableOpacity onPress={handleSkip}>
        <Text>Skip</Text>
      </TouchableOpacity>
    </View>
  );
};

export const NextBackTwo: React.FC<ButtonProps> = ({
  handleNext,
  handleSkip,
}) => {
  return (
    <View style={styles.nextBackContainer}>
      <TouchableOpacity onPress={handleSkip}>
        <Text style={styles.skipText}>Skip</Text>
      </TouchableOpacity>
      <ProgressTwo />
      <TouchableOpacity onPress={handleNext}>
        <Text style={styles.nextText}>Next</Text>
      </TouchableOpacity>
    </View>
  );
};

export const NextBackThree: React.FC<ButtonProps> = ({
  handleNext,
  handleSkip,
}) => {
  return (
    <View style={styles.nextBackContainer}>
      <TouchableOpacity onPress={handleSkip}>
        <Text style={styles.skipText}>Skip</Text>
      </TouchableOpacity>
      <ProgressThree />
      <TouchableOpacity onPress={handleNext}>
        <Text style={styles.nextText}>Next</Text>
      </TouchableOpacity>
    </View>
  );
};

export const NextBackFour: React.FC<ButtonProps> = ({
  handleNext,
  handleSkip,
}) => {
  return (
    <View style={styles.nextBackContainer}>
      <TouchableOpacity onPress={handleSkip}>
        <Text style={styles.skipText}>Skip</Text>
      </TouchableOpacity>
      <ProgressFour />
      <TouchableOpacity onPress={handleNext}>
        <Text style={styles.nextText}>Next</Text>
      </TouchableOpacity>
    </View>
  );
};

export const SkipNextButton: React.FC<SkipNextButtonProps> = ({
  handleNext,
  handleSkip,
  index,
}) => {
  return (
    <View style={styles.container}>
      <ProgressOne index={index} />
      <TouchableOpacity onPress={handleNext} style={styles.nextButton}>
        <Text style={styles.nextButtonText}>Next</Text>
      </TouchableOpacity>
      {index !== 3 && (
        <TouchableOpacity onPress={handleSkip} style={styles.skipButton}>
          <Text style={styles.skipButtonText}>Skip</Text>
        </TouchableOpacity>
      )}
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    alignItems: "center",
    justifyContent: "center",
    width: SIZES.width,
  },
  button: {
    alignItems: "center",
    justifyContent: "center",
    marginVertical: 10,
  },
  nextBackContainer: {
    marginTop: 75,
    flexDirection: "row",
    justifyContent: "space-between",
    bottom: "1%",
  },
  skipText: {
    paddingLeft: 40,
  },
  nextText: {
    paddingRight: 40,
  },
  nextButton: {
    backgroundColor: COLORS.primary,
    borderRadius: 15,
    flexDirection: "row",
    alignItems: "center",
    justifyContent: "center",
    width: "90%",
    marginVertical: SIZES.height * 0.026824,
    height: SIZES.height * 0.068,
    borderColor: COLORS.primary,
  },
  skipButton: {},
  nextButtonText: {
    color: COLORS.white,
    fontSize: 15,
    fontWeight: "500",
    textAlign: "center",
  },
  skipButtonText: {
    color: COLORS.primary,
    fontSize: 15,
    fontWeight: "500",
    textAlign: "center",
  },
});

export default {
  NextBackOne,
  NextBackTwo,
  NextBackThree,
  NextBackFour,
  SkipNextButton,
};
