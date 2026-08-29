// The add-product / add-service form. `?index` selects which: 0 = product,
// 1 = service. Opened from the Services hub and from the merchant dashboard,
// which is why it lives here rather than in the Services tab.
import { View } from 'react-native';
import { useLocalSearchParams } from 'expo-router';
import AddProduct from '@/components/products/AddProduct';
import AddService from '@/components/services/AddService';

const ServiceAction = () => {
  const { index } = useLocalSearchParams<{ index?: string }>();
  return (
    <View style={{ flex: 1 }}>
      {index === '0' && <AddProduct />}
      {index === '1' && <AddService />}
    </View>
  );
};

export default ServiceAction;
