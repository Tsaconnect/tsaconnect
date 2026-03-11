import P2PSellScreen from '@/screens/sellP2P';
import React, { useState } from 'react';
import {

View,
Text,
StyleSheet,
ScrollView,
TextInput,
TouchableOpacity,
Alert,
} from 'react-native';

export default function SellP2P() {
const [amount, setAmount] = useState('');
const [price, setPrice] = useState('');
const [description, setDescription] = useState('');

const handleCreateListing = () => {
    if (!amount || !price) {
        Alert.alert('Error', 'Please fill in all required fields');
        return;
    }
    Alert.alert('Success', 'Listing created successfully');
    setAmount('');
    setPrice('');
    setDescription('');
};

return (
    <P2PSellScreen/>
);
}
/* 
const styles = StyleSheet.create({
container: {
    flex: 1,
    backgroundColor: '#fff',
    padding: 16,
},
title: {
    fontSize: 24,
    fontWeight: 'bold',
    marginBottom: 20,
},
form: {
    gap: 16,
},
label: {
    fontSize: 14,
    fontWeight: '600',
    marginBottom: 8,
},
input: {
    borderWidth: 1,
    borderColor: '#ddd',
    borderRadius: 8,
    padding: 12,
    fontSize: 14,
},
textArea: {
    textAlignVertical: 'top',
},
button: {
    backgroundColor: '#007AFF',
    padding: 16,
    borderRadius: 8,
    alignItems: 'center',
    marginTop: 8,
},
buttonText: {
    color: '#fff',
    fontSize: 16,
    fontWeight: '600',
},
}); */