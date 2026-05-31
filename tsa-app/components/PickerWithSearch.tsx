// PickerWithSearch.tsx
//
// Generic searchable picker. Migrated onto the TSA Signature design system:
// the inline trigger is now a `SelectRow` and the modal is delegated to the
// shared `PickerSheet` (which already provides the search/filter + grouped
// list behavior). The exported API — `data` ({label,value}[]), `selectedValue`,
// `onSelect(item)`, `placeholder`, `style`, `searchable` — and all behavior
// (auto-select when there is exactly one option, search-by-label/value,
// disabled trigger when there is a single option) are preserved verbatim so
// existing consumers (screens/dash.tsx) keep working unchanged.
import React, { useEffect, useMemo, useState } from 'react';
import { ViewStyle } from 'react-native';
import { PickerSheet, SelectRow, type PickerItem as SheetPickerItem } from '@/components/ui';

type PickerItem = {
  label: string;
  value: string;
};

type PickerWithSearchProps = {
  data: PickerItem[];
  selectedValue: string;
  onSelect: (item: PickerItem) => void;
  placeholder?: string;
  style?: ViewStyle;
  searchable?: boolean;
};

const PickerWithSearch = ({
  data,
  selectedValue,
  onSelect,
  placeholder = 'Select an option',
  style,
  searchable = true,
}: PickerWithSearchProps) => {
  const [modalVisible, setModalVisible] = useState(false);

  // Auto-select the sole option so callers never sit on an unset value when
  // there is only one possible choice. (Behavior preserved from the original.)
  useEffect(() => {
    if (data.length === 1 && selectedValue !== data[0].value) {
      onSelect(data[0]);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data]);

  const selectedItem = data.find((item) => item.value === selectedValue);

  // Map {label, value} → PickerSheet's {id, label}; `value` is the stable id so
  // PickerSheet's label/sublabel search still matches the original label/value
  // filter (value === id, label === label).
  const sheetItems = useMemo<SheetPickerItem[]>(
    () => data.map((item) => ({ id: item.value, label: item.label, sublabel: item.value })),
    [data],
  );

  const handleSelect = (item: SheetPickerItem) => {
    onSelect({ label: item.label, value: item.id });
    setModalVisible(false);
  };

  return (
    <>
      <SelectRow
        value={selectedItem?.label}
        placeholder={placeholder}
        disabled={data.length <= 1}
        onPress={() => data.length > 1 && setModalVisible(true)}
        style={style}
      />

      <PickerSheet
        visible={modalVisible}
        onClose={() => setModalVisible(false)}
        title="Select Asset"
        items={sheetItems}
        selectedId={selectedValue}
        onSelect={handleSelect}
        searchable={searchable}
      />
    </>
  );
};

export { PickerWithSearch };
