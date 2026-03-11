import { StyleSheet, Text, View, TouchableOpacity } from 'react-native';
import React from 'react';
import { SIZES } from '../../constants';

interface NavTabProps {
  handlPress: () => void;
  title: string;
}

const NavTab: React.FC<NavTabProps> = ({ handlPress, title }) => {
  return (
    <View style={styles.buttonContainer}>
      <TouchableOpacity onPress={handlPress}>
        <Text style={styles.buttonText}>{title}</Text>
      </TouchableOpacity>
    </View>
  );
};

export default NavTab;

const styles = StyleSheet.create({
  buttonContainer: {
    height: SIZES.height * 0.05579399,
    alignItems: 'center',
    justifyContent: 'center',
    width: SIZES.width * 0.9069767,
    borderRadius: 10,
    marginTop: 15,
  },
  buttonText: {
    fontSize: 14,
    fontWeight: '500' as '500', // Ensure fontWeight type compatibility
  },
});
