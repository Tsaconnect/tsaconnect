// app/admin/spot-listings/add.tsx — create a new Spot listing.
import React from 'react';
import SpotListingForm from '@/components/spot/SpotListingForm';

export default function AddSpotListingScreen() {
  return <SpotListingForm mode="create" />;
}
