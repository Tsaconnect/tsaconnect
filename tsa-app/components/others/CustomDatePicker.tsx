import React, { useState } from 'react';
import { View, Text, TouchableOpacity, Platform, StyleSheet } from 'react-native';
import DateTimePickerModal from 'react-native-modal-datetime-picker';
import moment from 'moment';

const CustomDatePicker = ({ date, placeholder, onDateChange }) => {
  const [isDatePickerVisible, setDatePickerVisibility] = useState(false);
  const [selectedDate, setSelectedDate] = useState(date || new Date());
  const [dateString, setDateString] = useState(date ? moment(date).format('YYYY-MM-DD') : '');

  const showDatePicker = () => {
    setDatePickerVisibility(true);
  };

  const hideDatePicker = () => {
    setDatePickerVisibility(false);
  };

  const handleConfirm = (date) => {
    setSelectedDate(date);
    setDateString(moment(date).format('YYYY-MM-DD'));
    onDateChange(date);
    hideDatePicker();
  };

  return (
    <View style={{ flex: 1 }}>
      <TouchableOpacity onPress={showDatePicker} style={styles.inputContainerStyle}>
        {dateString ? (
          <Text style={styles.textStyle}>{dateString}</Text>
        ) : (
          <Text style={styles.placeholderStyle}>{placeholder || 'Select Date'}</Text>
        )}
      </TouchableOpacity>
      <DateTimePickerModal
        isVisible={isDatePickerVisible}
        mode="date"
        date={selectedDate}
        onConfirm={handleConfirm}
        onCancel={hideDatePicker}
      />
    </View>
  );
};

const styles = StyleSheet.create({
  inputContainerStyle: {
    alignItems: 'flex-start',
    justifyContent: 'center',
    borderWidth: 1,
    borderColor: '#CAD3DF',
    borderRadius: 5,
    marginVertical: 10,
    marginHorizontal: 10,
    paddingRight: 10,
    height: 50,
  },
  placeholderStyle: {
    fontSize: 16,
    color: '#CDCDCD',
    marginHorizontal: 10,
  },
  textStyle: {
    fontSize: 16,
    marginHorizontal: 10,
  },
});

export default CustomDatePicker;
