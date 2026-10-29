// The add-product / add-service form. `?index` selects which: 0 = product,
// 1 = service. Opened from the Services hub and from the merchant dashboard,
// which is why it lives here rather than in the Services tab.
//
// `?productId` switches the service form into edit mode. AddService has always
// supported this — it prefetches the product, profile, menu and availability
// and PUTs instead of POSTing — but nothing passed the id, so a merchant's only
// edit route was the product editor, which cannot see any service config. That
// left booking fields, hours, policies and fees write-once at creation.
import { View } from 'react-native';
import { Stack, useLocalSearchParams } from 'expo-router';
import AddProduct from '@/components/products/AddProduct';
import AddService from '@/components/services/AddService';

const ServiceAction = () => {
  const { index, productId } = useLocalSearchParams<{ index?: string; productId?: string }>();
  return (
    <View style={{ flex: 1 }}>
      {/* The layout's title names the add flow; an edit says so. */}
      {productId ? <Stack.Screen options={{ title: 'Edit listing' }} /> : null}
      {index === '0' && <AddProduct />}
      {index === '1' && <AddService productId={productId} />}
    </View>
  );
};

export default ServiceAction;
