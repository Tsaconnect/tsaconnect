import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { getCategories, createCategory, updateCategory, deleteCategory } from '@/api/categories';
import { PageHeader } from '@/components/shared/page-header';
import { ConfirmDialog } from '@/components/shared/confirm-dialog';
import { StatusBadge } from '@/components/shared/status-badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from '@/components/ui/dialog';
import { Card, CardContent } from '@/components/ui/card';
import { Plus, Pencil, Trash2, FolderTree, Upload } from 'lucide-react';
import { toast } from 'sonner';
import { usePermission } from '@/hooks/use-permission';
import type { Category } from '@/types';

export default function CategoriesPage() {
  const queryClient = useQueryClient();
  const canCreate = usePermission('categories.create');
  const canEdit = usePermission('categories.edit');
  const canDelete = usePermission('categories.delete');

  const [typeFilter, setTypeFilter] = useState('');
  const [modalOpen, setModalOpen] = useState(false);
  const [editingCategory, setEditingCategory] = useState<Category | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Category | null>(null);
  const [form, setForm] = useState({ title: '', description: '', type: 'Product' as string, isActive: true });
  const [imageFile, setImageFile] = useState<File | null>(null);
  const [imagePreview, setImagePreview] = useState<string | null>(null);

  const { data, isLoading } = useQuery({
    queryKey: ['categories', typeFilter],
    queryFn: () => getCategories({ type: typeFilter || undefined, active: undefined }),
  });

  const categories = data?.data ?? [];

  const saveMutation = useMutation({
    mutationFn: (formData: FormData) =>
      editingCategory ? updateCategory(editingCategory.id, formData) : createCategory(formData),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['categories'] });
      setModalOpen(false);
      setEditingCategory(null);
      toast.success(editingCategory ? 'Category updated' : 'Category created');
    },
    onError: () => toast.error('Failed to save category'),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteCategory(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['categories'] });
      setDeleteTarget(null);
      toast.success('Category deleted');
    },
  });

  const handleSave = () => {
    const fd = new FormData();
    fd.append('title', form.title);
    fd.append('description', form.description);
    fd.append('type', form.type);
    fd.append('isActive', String(form.isActive));
    if (imageFile) {
      fd.append('image', imageFile);
    }
    saveMutation.mutate(fd);
  };

  const handleImageChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (file) {
      setImageFile(file);
      setImagePreview(URL.createObjectURL(file));
    }
  };

  const openEdit = (cat: Category) => {
    setEditingCategory(cat);
    setForm({ title: cat.title, description: cat.description || '', type: cat.type, isActive: cat.isActive });
    setImageFile(null);
    setImagePreview(cat.image || null);
    setModalOpen(true);
  };

  const openCreate = () => {
    setEditingCategory(null);
    setForm({ title: '', description: '', type: 'Product', isActive: true });
    setImageFile(null);
    setImagePreview(null);
    setModalOpen(true);
  };

  const parentCats = categories.filter((c: Category) => !c.parentCategory);
  const childCats = (parentId: string) => categories.filter((c: Category) => c.parentCategory === parentId);

  return (
    <div>
      <PageHeader
        title="Categories"
        description="Manage product and service categories"
        actions={canCreate ? <Button onClick={openCreate}><Plus className="mr-2 h-4 w-4" />Add Category</Button> : undefined}
      />

      <div className="mb-4">
        <Select value={typeFilter} onValueChange={(v) => setTypeFilter(v === 'all' ? '' : v)}>
          <SelectTrigger className="w-40"><SelectValue placeholder="All types" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All types</SelectItem>
            <SelectItem value="Product">Product</SelectItem>
            <SelectItem value="Service">Service</SelectItem>
            <SelectItem value="Both">Both</SelectItem>
          </SelectContent>
        </Select>
      </div>

      {isLoading ? (
        <div className="space-y-2">{Array.from({ length: 5 }).map((_, i) => <div key={i} className="h-12 bg-slate-100 animate-pulse rounded" />)}</div>
      ) : (
        <div className="space-y-3">
          {parentCats.map((cat: Category) => (
            <Card key={cat.id}>
              <CardContent className="p-4">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-3">
                    {cat.image ? (
                      <img src={cat.image} alt={cat.title} className="h-10 w-10 rounded-lg object-cover" />
                    ) : (
                      <FolderTree className="h-5 w-5 text-blue-600" />
                    )}
                    <div>
                      <p className="font-medium text-slate-900">{cat.title}</p>
                      <p className="text-xs text-slate-500">{cat.type} · {cat.productCount} products</p>
                    </div>
                    <StatusBadge status={cat.isActive ? 'active' : 'inactive'} colorMap={{ active: 'bg-green-100 text-green-700', inactive: 'bg-slate-100 text-slate-700' }} />
                  </div>
                  <div className="flex gap-1">
                    {canEdit && <Button variant="ghost" size="icon" onClick={() => openEdit(cat)}><Pencil className="h-4 w-4" /></Button>}
                    {canDelete && <Button variant="ghost" size="icon" onClick={() => setDeleteTarget(cat)}><Trash2 className="h-4 w-4 text-red-500" /></Button>}
                  </div>
                </div>
                {childCats(cat.id).length > 0 && (
                  <div className="mt-3 ml-8 space-y-2">
                    {childCats(cat.id).map((child: Category) => (
                      <div key={child.id} className="flex items-center justify-between rounded border border-slate-100 p-2">
                        <span className="text-sm text-slate-700">{child.title}</span>
                        <div className="flex gap-1">
                          {canEdit && <Button variant="ghost" size="sm" onClick={() => openEdit(child)}><Pencil className="h-3 w-3" /></Button>}
                          {canDelete && <Button variant="ghost" size="sm" onClick={() => setDeleteTarget(child)}><Trash2 className="h-3 w-3 text-red-500" /></Button>}
                        </div>
                      </div>
                    ))}
                  </div>
                )}
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      <Dialog open={modalOpen} onOpenChange={setModalOpen}>
        <DialogContent>
          <DialogHeader><DialogTitle>{editingCategory ? 'Edit Category' : 'Create Category'}</DialogTitle></DialogHeader>
          <div className="space-y-4">
            <div><Label>Title</Label><Input value={form.title} onChange={(e) => setForm({ ...form, title: e.target.value })} /></div>
            <div><Label>Description</Label><Input value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} /></div>
            <div>
              <Label>Type</Label>
              <Select value={form.type} onValueChange={(v) => setForm({ ...form, type: v })}>
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="Product">Product</SelectItem>
                  <SelectItem value="Service">Service</SelectItem>
                  <SelectItem value="Both">Both</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div>
              <Label>Image</Label>
              <div className="mt-1">
                {imagePreview && (
                  <div className="mb-2 relative w-full h-32 rounded-lg overflow-hidden border border-slate-200">
                    <img src={imagePreview} alt="Preview" className="w-full h-full object-cover" />
                  </div>
                )}
                <label className="flex items-center gap-2 cursor-pointer rounded-lg border border-dashed border-slate-300 p-3 hover:border-blue-400 transition-colors">
                  <Upload className="h-4 w-4 text-slate-500" />
                  <span className="text-sm text-slate-600">{imageFile ? imageFile.name : 'Choose image...'}</span>
                  <input type="file" accept="image/*" className="hidden" onChange={handleImageChange} />
                </label>
              </div>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setModalOpen(false)}>Cancel</Button>
            <Button onClick={handleSave} disabled={saveMutation.isPending}>{saveMutation.isPending ? 'Saving...' : 'Save'}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={!!deleteTarget}
        onOpenChange={() => setDeleteTarget(null)}
        title="Delete Category"
        description={`Are you sure you want to delete "${deleteTarget?.title}"? This cannot be undone.`}
        confirmLabel="Delete"
        variant="destructive"
        loading={deleteMutation.isPending}
        onConfirm={() => deleteTarget && deleteMutation.mutate(deleteTarget.id)}
      />
    </div>
  );
}
