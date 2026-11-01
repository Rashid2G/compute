import { useEffect, useMemo, useState } from 'react';
import {
  Box,
  Copy,
  Disc,
  Layers,
  Loader2,
  Package,
  Play,
  Plus,
  RefreshCw,
  Search,
  Server,
  Trash2,
} from 'lucide-react';
import { toast } from 'sonner';

import { CopyButton } from '@/components/copy-button';
import { EmptyState } from '@/components/empty-state';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip';
import { api, errorMessage } from '@/lib/api';
import { formatDateTime, formatMemory, relativeTime } from '@/lib/format';
import type {
  BuildImageRequest,
  CreateTemplateRequest,
  CustomImage,
  Template,
  VM,
} from '@/lib/types';
import { cn } from '@/lib/utils';

export function ImagesTemplatesPage({
  vms,
  onLaunchVM,
}: {
  vms: VM[];
  onLaunchVM?: (templateOrImage: { template?: string; image?: string }) => void;
}) {
  const [tab, setTab] = useState<'images' | 'templates'>('images');
  const [images, setImages] = useState<CustomImage[]>([]);
  const [templates, setTemplates] = useState<Template[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [searchQuery, setSearchQuery] = useState('');

  // Dialog States
  const [buildImageOpen, setBuildImageOpen] = useState(false);
  const [createTemplateOpen, setCreateTemplateOpen] = useState(false);
  const [launchTemplateTarget, setLaunchTemplateTarget] = useState<Template | null>(null);

  // Form states: Build Image
  const [imgName, setImgName] = useState('');
  const [imgBase, setImgBase] = useState('ubuntu-24');
  const [imgPackages, setImgPackages] = useState('curl, git, htop, build-essential');
  const [imgDiskGB, setImgDiskGB] = useState<number>(50);
  const [isBuilding, setIsBuilding] = useState(false);

  // Form states: Create Template
  const [tmplSourceVM, setTmplSourceVM] = useState('');
  const [tmplName, setTmplName] = useState('');
  const [tmplDesc, setTmplDesc] = useState('');
  const [isCreatingTmpl, setIsCreatingTmpl] = useState(false);

  // Form states: Launch Template
  const [launchVMName, setLaunchVMName] = useState('');
  const [isLaunching, setIsLaunching] = useState(false);

  async function loadData(silent = false) {
    if (!silent) setIsRefreshing(true);
    try {
      const [imgRes, tmplRes] = await Promise.allSettled([
        api.listImages(),
        api.listTemplates(),
      ]);
      if (imgRes.status === 'fulfilled') {
        setImages(imgRes.value);
      }
      if (tmplRes.status === 'fulfilled') {
        setTemplates(tmplRes.value);
      }
    } finally {
      setIsLoading(false);
      setIsRefreshing(false);
    }
  }

  useEffect(() => {
    void loadData();
  }, []);

  async function handleBuildImage(e: React.FormEvent) {
    e.preventDefault();
    if (!imgName.trim()) {
      toast.error('Image name is required');
      return;
    }
    const pkgs = imgPackages
      .split(/[\s,]+/)
      .map((p) => p.trim())
      .filter(Boolean);

    if (pkgs.length === 0) {
      toast.error('At least one package is required');
      return;
    }

    setIsBuilding(true);
    const toastId = toast.loading(`Building image ${imgName.trim()} with packages...`);
    try {
      await api.buildImage({
        name: imgName.trim(),
        base_image: imgBase,
        packages: pkgs,
        disk_size_mb: imgDiskGB * 1024,
      });
      toast.success(`Image ${imgName.trim()} built successfully!`, { id: toastId });
      setBuildImageOpen(false);
      setImgName('');
      setImgPackages('curl, git, htop, build-essential');
      await loadData(true);
    } catch (err) {
      toast.error('Failed to build image', {
        id: toastId,
        description: errorMessage(err),
      });
    } finally {
      setIsBuilding(false);
    }
  }

  async function handleDeleteImage(img: CustomImage) {
    if (!confirm(`Are you sure you want to delete image "${img.name}"?`)) return;
    const toastId = toast.loading(`Deleting image ${img.name}...`);
    try {
      await api.deleteImage(img.id);
      toast.success(`Image ${img.name} deleted`, { id: toastId });
      await loadData(true);
    } catch (err) {
      toast.error('Failed to delete image', {
        id: toastId,
        description: errorMessage(err),
      });
    }
  }

  async function handleCreateTemplate(e: React.FormEvent) {
    e.preventDefault();
    if (!tmplSourceVM) {
      toast.error('Please select a source VM');
      return;
    }
    if (!tmplName.trim()) {
      toast.error('Template name is required');
      return;
    }

    setIsCreatingTmpl(true);
    const toastId = toast.loading(`Freezing VM disk and creating template "${tmplName.trim()}"...`);
    try {
      await api.createTemplate({
        vm_id: tmplSourceVM,
        name: tmplName.trim(),
        description: tmplDesc.trim(),
      });
      toast.success(`Template ${tmplName.trim()} created!`, { id: toastId });
      setCreateTemplateOpen(false);
      setTmplSourceVM('');
      setTmplName('');
      setTmplDesc('');
      setTab('templates');
      await loadData(true);
    } catch (err) {
      toast.error('Failed to create template', {
        id: toastId,
        description: errorMessage(err),
      });
    } finally {
      setIsCreatingTmpl(false);
    }
  }

  async function handleDeleteTemplate(tmpl: Template) {
    if (!confirm(`Are you sure you want to delete template "${tmpl.name}"?`)) return;
    const toastId = toast.loading(`Deleting template ${tmpl.name}...`);
    try {
      await api.deleteTemplate(tmpl.id);
      toast.success(`Template ${tmpl.name} deleted`, { id: toastId });
      await loadData(true);
    } catch (err) {
      toast.error('Failed to delete template', {
        id: toastId,
        description: errorMessage(err),
      });
    }
  }

  async function handleLaunchTemplate(e: React.FormEvent) {
    e.preventDefault();
    if (!launchTemplateTarget) return;
    const name = launchVMName.trim() || `vm-${launchTemplateTarget.name}-${Math.random().toString(36).slice(2, 6)}`;

    setIsLaunching(true);
    const toastId = toast.loading(`Launching instance "${name}" from template...`);
    try {
      await api.launchTemplate(launchTemplateTarget.id, {
        name,
        vcpus: launchTemplateTarget.vcpus,
        memory_mb: launchTemplateTarget.memory_mb,
      });
      toast.success(`Instance ${name} launched from template!`, { id: toastId });
      setLaunchTemplateTarget(null);
      setLaunchVMName('');
    } catch (err) {
      toast.error('Failed to launch instance', {
        id: toastId,
        description: errorMessage(err),
      });
    } finally {
      setIsLaunching(false);
    }
  }

  const filteredImages = useMemo(() => {
    const q = searchQuery.toLowerCase().trim();
    if (!q) return images;
    return images.filter(
      (img) =>
        img.name.toLowerCase().includes(q) ||
        img.id.toLowerCase().includes(q) ||
        img.packages?.some((p) => p.toLowerCase().includes(q)),
    );
  }, [images, searchQuery]);

  const filteredTemplates = useMemo(() => {
    const q = searchQuery.toLowerCase().trim();
    if (!q) return templates;
    return templates.filter(
      (t) =>
        t.name.toLowerCase().includes(q) ||
        t.id.toLowerCase().includes(q) ||
        t.description?.toLowerCase().includes(q),
    );
  }, [templates, searchQuery]);

  return (
    <div className="space-y-4 animate-fade-in">
      {/* Tabs, Search & Actions */}
      <Tabs value={tab} onValueChange={(val) => setTab(val as 'images' | 'templates')}>
        <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
          <TabsList className="h-auto w-full flex-wrap justify-start gap-0.5 bg-transparent p-0 lg:w-auto">
            <TabsTrigger
              value="images"
              className="rounded-md border border-transparent px-3 py-1.5 gap-2 data-[state=active]:border-border data-[state=active]:bg-card"
            >
              <Disc className="h-4 w-4" />
              <span>Custom Images</span>
              <span className="tabular rounded bg-secondary px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
                {images.length}
              </span>
            </TabsTrigger>
            <TabsTrigger
              value="templates"
              className="rounded-md border border-transparent px-3 py-1.5 gap-2 data-[state=active]:border-border data-[state=active]:bg-card"
            >
              <Layers className="h-4 w-4" />
              <span>Disk Templates</span>
              <span className="tabular rounded bg-secondary px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
                {templates.length}
              </span>
            </TabsTrigger>
          </TabsList>

          <div className="flex flex-wrap items-center gap-2">
            <div className="relative w-full sm:w-64">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                placeholder={`Search ${tab === 'images' ? 'images' : 'templates'}…`}
                className="pl-8 text-xs"
                aria-label="Search images and templates"
              />
            </div>
            <Button
              variant="outline"
              size="sm"
              onClick={() => setCreateTemplateOpen(true)}
            >
              <Layers className="mr-1.5 h-4 w-4" />
              <span>Template from VM</span>
            </Button>
            <Button
              size="sm"
              onClick={() => setBuildImageOpen(true)}
            >
              <Plus className="mr-1.5 h-4 w-4" />
              <span>Build Image</span>
            </Button>
          </div>
        </div>

        {/* Tab 1: Images */}
        <TabsContent value="images" className="mt-4">
          <Card>
            <CardHeader className="p-4 pb-2">
              <CardTitle className="text-sm font-medium">Pre-baked Custom Images</CardTitle>
              <CardDescription className="text-xs">
                Root filesystems formatted as ext4 with requested apt packages pre-installed.
              </CardDescription>
            </CardHeader>
            <CardContent className="p-0">
              {filteredImages.length === 0 ? (
                <div className="p-8">
                  <EmptyState icon={Disc}
                    title="No custom images found"
                    description="Build a custom image with your preferred packages to quickly provision specialized microVMs."
                  />
                </div>
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Image</TableHead>
                      <TableHead>Base OS</TableHead>
                      <TableHead>Status</TableHead>
                      <TableHead>Pre-installed Packages</TableHead>
                      <TableHead>Disk Size</TableHead>
                      <TableHead>Created</TableHead>
                      <TableHead className="text-right">Actions</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {filteredImages.map((img) => (
                      <TableRow key={img.id}>
                        <TableCell>
                          <div className="flex items-center gap-2">
                            <Disc className="h-4 w-4 text-primary shrink-0" />
                            <div>
                              <div className="font-medium text-xs text-foreground">{img.name}</div>
                              <div className="flex items-center gap-1 font-mono text-[10px] text-muted-foreground">
                                <span>{img.id}</span>
                                <CopyButton value={img.id} label="Copy ID" className="h-4 w-4" />
                              </div>
                            </div>
                          </div>
                        </TableCell>
                        <TableCell>
                          <Badge variant="secondary" className="font-mono text-[10px]">
                            {img.base_image || 'ubuntu-24'}
                          </Badge>
                        </TableCell>
                        <TableCell>
                          {img.status === 'ready' ? (
                            <Badge variant="success">Ready</Badge>
                          ) : img.status === 'building' ? (
                            <Badge variant="warning">
                              <Loader2 className="h-3 w-3 animate-spin mr-1" />
                              Building
                            </Badge>
                          ) : (
                            <Badge variant="destructive">Failed</Badge>
                          )}
                        </TableCell>
                        <TableCell>
                          <div className="flex flex-wrap gap-1 max-w-xs">
                            {img.packages && img.packages.length > 0 ? (
                              img.packages.map((pkg) => (
                                <span
                                  key={pkg}
                                  className="rounded bg-secondary/80 px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground"
                                >
                                  {pkg}
                                </span>
                              ))
                            ) : (
                              <span className="text-[11px] text-muted-foreground">None</span>
                            )}
                          </div>
                        </TableCell>
                        <TableCell className="font-mono text-xs">
                          {Math.round(img.disk_size_mb / 1024)} GB
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground">
                          {relativeTime(img.created_at)}
                        </TableCell>
                        <TableCell className="text-right">
                          <div className="flex items-center justify-end gap-1">
                            {onLaunchVM ? (
                              <Tooltip>
                                <TooltipTrigger asChild>
                                  <Button
                                    variant="outline"
                                    size="icon-sm"
                                    onClick={() => onLaunchVM({ image: img.name })}
                                  >
                                    <Play className="h-3.5 w-3.5 text-primary" />
                                  </Button>
                                </TooltipTrigger>
                                <TooltipContent>Provision VM with this image</TooltipContent>
                              </Tooltip>
                            ) : null}
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <Button
                                  variant="ghost"
                                  size="icon-sm"
                                  className="text-destructive hover:bg-destructive/10"
                                  onClick={() => handleDeleteImage(img)}
                                >
                                  <Trash2 className="h-3.5 w-3.5" />
                                </Button>
                              </TooltipTrigger>
                              <TooltipContent>Delete Image</TooltipContent>
                            </Tooltip>
                          </div>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        {/* Tab 2: Templates */}
        <TabsContent value="templates" className="mt-4">
          <Card>
            <CardHeader className="p-4 pb-2">
              <CardTitle className="text-sm font-medium">VM Disk Templates</CardTitle>
              <CardDescription className="text-xs">
                Snapshots of modified VM disks frozen into reusable templates for rapid deployment.
              </CardDescription>
            </CardHeader>
            <CardContent className="p-0">
              {filteredTemplates.length === 0 ? (
                <div className="p-8">
                  <EmptyState icon={Layers}
                    title="No templates found"
                    description="Launch any VM, install or customize your files via the shell, and create a reusable template."
                  />
                </div>
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Template</TableHead>
                      <TableHead>Description</TableHead>
                      <TableHead>Source VM</TableHead>
                      <TableHead>Baseline Specs</TableHead>
                      <TableHead>Created</TableHead>
                      <TableHead className="text-right">Actions</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {filteredTemplates.map((tmpl) => (
                      <TableRow key={tmpl.id}>
                        <TableCell>
                          <div className="flex items-center gap-2">
                            <Layers className="h-4 w-4 text-info shrink-0" />
                            <div>
                              <div className="font-medium text-xs text-foreground">{tmpl.name}</div>
                              <div className="flex items-center gap-1 font-mono text-[10px] text-muted-foreground">
                                <span>{tmpl.id}</span>
                                <CopyButton value={tmpl.id} label="Copy ID" className="h-4 w-4" />
                              </div>
                            </div>
                          </div>
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground max-w-xs truncate">
                          {tmpl.description || 'No description provided'}
                        </TableCell>
                        <TableCell>
                          <span className="font-mono text-[11px] text-muted-foreground">
                            {tmpl.source_vm_id || '-'}
                          </span>
                        </TableCell>
                        <TableCell>
                          <div className="font-mono text-[11px]">
                            {tmpl.vcpus} vCPU &middot; {formatMemory(tmpl.memory_mb)} &middot;{' '}
                            {Math.round(tmpl.disk_size_mb / 1024)} GB
                          </div>
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground">
                          {relativeTime(tmpl.created_at)}
                        </TableCell>
                        <TableCell className="text-right">
                          <div className="flex items-center justify-end gap-1">
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <Button
                                  variant="outline"
                                  size="icon-sm"
                                  onClick={() => {
                                    setLaunchTemplateTarget(tmpl);
                                    setLaunchVMName(`vm-${tmpl.name}-${Math.random().toString(36).slice(2, 6)}`);
                                  }}
                                >
                                  <Play className="h-3.5 w-3.5 text-primary" />
                                </Button>
                              </TooltipTrigger>
                              <TooltipContent>Launch instance from this template</TooltipContent>
                            </Tooltip>
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <Button
                                  variant="ghost"
                                  size="icon-sm"
                                  className="text-destructive hover:bg-destructive/10"
                                  onClick={() => handleDeleteTemplate(tmpl)}
                                >
                                  <Trash2 className="h-3.5 w-3.5" />
                                </Button>
                              </TooltipTrigger>
                              <TooltipContent>Delete Template</TooltipContent>
                            </Tooltip>
                          </div>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>

      {/* Dialog: Build Image */}
      <Dialog open={buildImageOpen} onOpenChange={setBuildImageOpen}>
        <DialogContent className="max-w-md">
          <form onSubmit={handleBuildImage} className="space-y-4">
            <DialogHeader>
              <DialogTitle className="flex items-center gap-2">
                <Disc className="h-5 w-5 text-primary" />
                Build Custom Image
              </DialogTitle>
              <DialogDescription className="text-xs">
                Build an Ubuntu ext4 rootfs disk with your desired packages pre-installed.
              </DialogDescription>
            </DialogHeader>

            <div className="space-y-3">
              <div className="space-y-1.5">
                <Label htmlFor="img-name" className="text-xs">
                  Image Name
                </Label>
                <Input
                  id="img-name"
                  value={imgName}
                  onChange={(e) => setImgName(e.target.value)}
                  placeholder="e.g. dev-tools"
                  className="font-mono text-xs"
                  required
                />
              </div>

              <div className="space-y-1.5">
                <Label htmlFor="img-base" className="text-xs">
                  Base Image
                </Label>
                <Select value={imgBase} onValueChange={setImgBase}>
                  <SelectTrigger id="img-base" className="text-xs">
                    <SelectValue placeholder="Base image" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="ubuntu-24">Ubuntu 24.04 LTS (Noble)</SelectItem>
                  </SelectContent>
                </Select>
              </div>

              <div className="space-y-1.5">
                <Label htmlFor="img-pkgs" className="text-xs">
                  Packages (comma or space separated)
                </Label>
                <Input
                  id="img-pkgs"
                  value={imgPackages}
                  onChange={(e) => setImgPackages(e.target.value)}
                  placeholder="e.g. curl, git, python3, nginx"
                  className="font-mono text-xs"
                  required
                />
                <p className="text-[10px] text-muted-foreground">
                  Packages will be installed non-interactively using apt-get inside the rootfs.
                </p>
              </div>

              <div className="space-y-1.5">
                <Label className="text-xs">Image Disk Size</Label>
                <div className="grid grid-cols-2 gap-2">
                  {[50, 100].map((size) => (
                    <button
                      key={size}
                      type="button"
                      onClick={() => setImgDiskGB(size)}
                      className={cn(
                        'rounded border p-2 text-left transition-colors',
                        imgDiskGB === size
                          ? 'border-primary bg-accent text-accent-foreground'
                          : 'bg-card hover:bg-secondary',
                      )}
                    >
                      <div className="text-xs font-semibold">{size} GB</div>
                      <div className="text-[10px] text-muted-foreground">
                        {size === 50 ? 'Standard' : 'Extended'}
                      </div>
                    </button>
                  ))}
                </div>
              </div>
            </div>

            <DialogFooter className="pt-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setBuildImageOpen(false)}
                disabled={isBuilding}
              >
                Cancel
              </Button>
              <Button type="submit" size="sm" disabled={isBuilding}>
                {isBuilding ? (
                  <>
                    <Loader2 className="h-3.5 w-3.5 mr-1.5 animate-spin" />
                    Building...
                  </>
                ) : (
                  'Build Image'
                )}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      {/* Dialog: Create Template from VM */}
      <Dialog open={createTemplateOpen} onOpenChange={setCreateTemplateOpen}>
        <DialogContent className="max-w-md">
          <form onSubmit={handleCreateTemplate} className="space-y-4">
            <DialogHeader>
              <DialogTitle className="flex items-center gap-2">
                <Layers className="h-5 w-5 text-info" />
                Create Template from VM
              </DialogTitle>
              <DialogDescription className="text-xs">
                Freezes the current disk state of an existing VM into a reusable template.
              </DialogDescription>
            </DialogHeader>

            <div className="space-y-3">
              <div className="space-y-1.5">
                <Label htmlFor="tmpl-source-vm" className="text-xs">
                  Source Instance
                </Label>
                <Select value={tmplSourceVM} onValueChange={setTmplSourceVM}>
                  <SelectTrigger id="tmpl-source-vm" className="text-xs">
                    <SelectValue placeholder="Select an instance" />
                  </SelectTrigger>
                  <SelectContent>
                    {vms.map((vm) => (
                      <SelectItem key={vm.id} value={vm.id}>
                        {vm.name} ({vm.id.slice(0, 8)}... - {vm.status})
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <p className="text-[10px] text-muted-foreground">
                  Filesystem buffers will be flushed prior to cloning the disk.
                </p>
              </div>

              <div className="space-y-1.5">
                <Label htmlFor="tmpl-name" className="text-xs">
                  Template Name
                </Label>
                <Input
                  id="tmpl-name"
                  value={tmplName}
                  onChange={(e) => setTmplName(e.target.value)}
                  placeholder="e.g. node20-configured"
                  className="font-mono text-xs"
                  required
                />
              </div>

              <div className="space-y-1.5">
                <Label htmlFor="tmpl-desc" className="text-xs">
                  Description (Optional)
                </Label>
                <Input
                  id="tmpl-desc"
                  value={tmplDesc}
                  onChange={(e) => setTmplDesc(e.target.value)}
                  placeholder="e.g. Node 20, pnpm, and custom dotfiles installed"
                  className="text-xs"
                />
              </div>
            </div>

            <DialogFooter className="pt-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setCreateTemplateOpen(false)}
                disabled={isCreatingTmpl}
              >
                Cancel
              </Button>
              <Button type="submit" size="sm" disabled={isCreatingTmpl}>
                {isCreatingTmpl ? (
                  <>
                    <Loader2 className="h-3.5 w-3.5 mr-1.5 animate-spin" />
                    Creating...
                  </>
                ) : (
                  'Create Template'
                )}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      {/* Dialog: Launch Template */}
      <Dialog
        open={launchTemplateTarget !== null}
        onOpenChange={(open) => !open && setLaunchTemplateTarget(null)}
      >
        <DialogContent className="max-w-md">
          <form onSubmit={handleLaunchTemplate} className="space-y-4">
            <DialogHeader>
              <DialogTitle className="flex items-center gap-2">
                <Play className="h-5 w-5 text-primary" />
                Launch Template
              </DialogTitle>
              <DialogDescription className="text-xs">
                Launch a new Firecracker microVM instance cloned from template &quot;
                {launchTemplateTarget?.name}&quot;.
              </DialogDescription>
            </DialogHeader>

            <div className="space-y-3">
              <div className="space-y-1.5">
                <Label htmlFor="launch-vm-name" className="text-xs">
                  Instance Name
                </Label>
                <Input
                  id="launch-vm-name"
                  value={launchVMName}
                  onChange={(e) => setLaunchVMName(e.target.value)}
                  placeholder="e.g. my-app-1"
                  className="font-mono text-xs"
                  required
                />
              </div>

              {launchTemplateTarget ? (
                <div className="rounded border bg-secondary/30 p-3 text-xs space-y-1">
                  <div className="font-medium text-foreground">Template Specs</div>
                  <div className="font-mono text-muted-foreground text-[11px]">
                    {launchTemplateTarget.vcpus} vCPU &middot;{' '}
                    {formatMemory(launchTemplateTarget.memory_mb)} &middot;{' '}
                    {Math.round(launchTemplateTarget.disk_size_mb / 1024)} GB Disk
                  </div>
                </div>
              ) : null}
            </div>

            <DialogFooter className="pt-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setLaunchTemplateTarget(null)}
                disabled={isLaunching}
              >
                Cancel
              </Button>
              <Button type="submit" size="sm" disabled={isLaunching}>
                {isLaunching ? (
                  <>
                    <Loader2 className="h-3.5 w-3.5 mr-1.5 animate-spin" />
                    Launching...
                  </>
                ) : (
                  'Launch Instance'
                )}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  );
}
