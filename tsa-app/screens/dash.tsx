// Dashboard.tsx
import React, { useState, useEffect } from 'react';
import {
  View,
  Text,
  ScrollView,
  StyleSheet,
  TouchableOpacity,
} from 'react-native';
import { COLORS, SIZES, FONTS } from '@/constants/theme';
import { PickerWithSearch } from '@/components/PickerWithSearch';
import { Ionicons } from '@expo/vector-icons'; // Assuming you're using Expo. If not, use appropriate icon library

type AssetType = 'mcgp' | 'usdt' | 'usdc';

const Dashboard = () => {
  // State for virtual balance
  const [virtualBalance, setVirtualBalance] = useState<string>('0.00');
  
  // State for selected asset
  const [selectedAsset, setSelectedAsset] = useState<AssetType>('mcgp');
  
  // State to track if balance is hidden
  const [isBalanceHidden, setIsBalanceHidden] = useState<boolean>(false);
  
  // Assets array for dropdown
  const assets: AssetType[] = ['mcgp', 'usdt', 'usdc'];
  
  // Mock function to fetch virtual balance - replace with actual API call
  const fetchVirtualBalance = async () => {
    // This would typically be an API call
    // For now, using mock data
    const mockBalance = '1,248.92';
    setVirtualBalance(mockBalance);
  };
  
  // Handle asset selection
  const handleAssetSelect = (asset: AssetType) => {
    setSelectedAsset(asset);
    // Here you would typically update the UI based on selected asset
    console.log(`Selected asset: ${asset}`);
  };
  
  // Toggle balance visibility
  const toggleBalanceVisibility = () => {
    setIsBalanceHidden(!isBalanceHidden);
  };
  
  // Get display balance based on visibility state
  const getDisplayBalance = () => {
    if (isBalanceHidden) {
      return '••••••';
    }
    return `$${virtualBalance}`;
  };
  
  // Fetch balance on component mount
  useEffect(() => {
    fetchVirtualBalance();
  }, []);
  
  return (
    <View style={{ backgroundColor: "#fff", flex: 1 }}>
      <ScrollView style={styles.cover}>
        {/* Header Section */}
        <View style={styles.headerContainer}>
          {/* Virtual Balance Display */}
          <View style={styles.balanceContainer}>
            <View style={styles.balanceHeaderRow}>
              <Text style={styles.labelHeader}>Virtual Balance:</Text>
              <TouchableOpacity
                onPress={toggleBalanceVisibility}
                style={styles.eyeIconContainer}
                activeOpacity={0.7}
              >
                <Ionicons
                  name={isBalanceHidden ? 'eye-off-outline' : 'eye-outline'}
                  size={SIZES.h3}
                  color={COLORS.primary}
                />
              </TouchableOpacity>
            </View>
            <Text style={styles.balanceText}>{getDisplayBalance()}</Text>
          </View>
          
          {/* Debit Account Selector */}
          <View style={styles.dropdownContainer}>
            <Text>Debit Account</Text>
            <PickerWithSearch
              data={assets.map(asset => ({
                label: asset.toUpperCase(),
                value: asset,
              }))}
              selectedValue={selectedAsset}
              onSelect={(item) => handleAssetSelect(item.value as AssetType)}
              placeholder="Select Asset"
              style={styles.dropdown}
              searchable={false} // Since we have only 3 options
            />
          </View>
        </View>
        
        {/* Main Content Area */}
        <View style={styles.contentContainer}>
          <Text style={styles.contentPlaceholder}>
            More dashboard content may go here later
          </Text>
          {/* Additional dashboard components can be added here */}
        </View>
      </ScrollView>
    </View>
  );
};

const styles = StyleSheet.create({
  cover: {
    flex: 1,
  },
  headerContainer: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingHorizontal: SIZES.width * 0.05,
    paddingVertical: SIZES.height * 0.02,
    borderBottomWidth: 1,
    borderBottomColor: COLORS.grey,
  },
  balanceContainer: {
   // flex: 2,
   
  },
  balanceHeaderRow: {
    flexDirection: 'row',
    alignItems: 'center',
     justifyContent: 'space-between',
    marginBottom: SIZES.base,
  },
  labelHeader: {
    ...FONTS.h4,
    color: COLORS.primary,
    fontWeight: 'bold',
  },
  eyeIconContainer: {
    padding: SIZES.base * 0.5,
    marginLeft: SIZES.base*2,
   
  },
  balanceText: {
    ...FONTS.h2,
    color: COLORS.primary,
    fontWeight: 'bold',
  },
  dropdownContainer: {
    width: SIZES.width * 0.3,
    marginLeft: SIZES.base,
  },
  dropdown: {
    borderWidth: 1,
    borderColor: COLORS.gray,
    borderRadius: SIZES.radius,
    paddingHorizontal: SIZES.base,
    paddingVertical: SIZES.base * 0.8,
    backgroundColor: COLORS.white,
  },
  contentContainer: {
    flex: 1,
    paddingHorizontal: SIZES.width * 0.05,
    paddingVertical: SIZES.height * 0.03,
    alignItems: 'center',
    justifyContent: 'center',
  },
  contentPlaceholder: {
    ...FONTS.body3,
    color: COLORS.gray,
    textAlign: 'center',
  },
});

export default Dashboard;