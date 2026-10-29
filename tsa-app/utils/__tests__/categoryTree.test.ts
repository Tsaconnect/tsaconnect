import { categoryPathLabels } from '../categoryTree';

describe('categoryPathLabels', () => {
  it('labels subcategories with their parents and sorts parents first', () => {
    const out = categoryPathLabels([
      { id: 'h', title: 'Student hostel', parentCategory: 'a' },
      { id: 'a', title: 'Accommodation' },
      { id: 'm', title: 'Mechanic', parentCategory: 'missing' },
    ]);
    expect(out.map((c) => c.path)).toEqual(['Accommodation', 'Accommodation › Student hostel', 'Mechanic']);
  });

  it('survives a parent loop in bad data', () => {
    const out = categoryPathLabels([
      { id: 'p', title: 'P', parentCategory: 'q' },
      { id: 'q', title: 'Q', parentCategory: 'p' },
    ]);
    expect(out.map((c) => c.path)).toEqual(['P › Q', 'Q › P']);
  });
});
