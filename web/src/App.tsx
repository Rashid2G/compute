import { useCallback, useEffect, useState } from 'react';
import { Toaster, toast } from 'sonner';

import { CommandPalette } from '@/components/command-palette';
import { CreateVMDialog } from '@/components/create-vm-dialog';
import { DeleteVMDialog } from '@/components/delete-vm-dialog';
import { InspectVMDialog } from '@/components/inspect-vm-dialog';
import { Sidebar } from '@/components/layout/sidebar';
import { Topbar } from '@/components/layout/topbar';
import { SSHKeysDialog } from '@/components/ssh-keys-dialog';
import { Sheet, SheetContent } from '@/components/ui/sheet';
import { TooltipProvider } from '@/components/ui/tooltip';
import { useCluster } from '@/hooks/use-cluster';
import { api, errorMessage } from '@/lib/api';
import { PAGES, resolvePageId, type PageId } from '@/lib/navigation';
import type { CreateVMRequest, VM } from '@/lib/types';
import { AccessPage } from '@/pages/access';
import { ApiPage } from '@/pages/api';
import { ConsolePage } from '@/pages/console';
import { DashboardPage } from '@/pages/dashboard';
import { EventsPage } from '@/pages/events';
import { ImagesTemplatesPage } from '@/pages/images-templates';
import { InstancesPage } from '@/pages/instances';
import { JobsPage } from '@/pages/jobs';
import { NetworkPage } from '@/pages/network';
import { SettingsPage } from '@/pages/settings';
import { SSHTerminalWindow } from '@/pages/ssh-terminal-window';
import { StoragePage } from '@/pages/storage';
import { WorkersPage } from '@/pages/workers';

type Theme = 'light' | 'dark';

function initialTheme(): Theme {
  const stored = window.localStorage.getItem('compute-theme');
  if (stored === 'light' || stored === 'dark') return stored;
  return 'dark';
}

function parseRoute(): { page: PageId | 'terminal'; vmId: string | null } {
  const rawHash = window.location.hash.replace(/^#\/?/, '');
  if (rawHash.startsWith('terminal/')) {
    const rawId = rawHash.slice('terminal/'.length).split('?')[0];
    return { page: 'terminal', vmId: decodeURIComponent(rawId) };
  }
  if (!rawHash) {
    return { page: 'instances', vmId: null };
  }
  return { page: resolvePageId(rawHash), vmId: null };
}

function pageFromHash(): PageId {
  const r = parseRoute();
  return r.page === 'terminal' ? 'instances' : r.page;
}

export default function App() {
  const [theme, setTheme] = useState<Theme>(initialTheme);
  const [route, setRoute] = useState(parseRoute);
  const [page, setPage] = useState<PageId>(pageFromHash);
  const [mobileNavOpen, setMobileNavOpen] = useState(false);

  const [createOpen, setCreateOpen] = useState(false);
  const [keysOpen, setKeysOpen] = useState(false);
  const [commandOpen, setCommandOpen] = useState(false);
  const [inspectTarget, setInspectTarget] = useState<VM | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<VM | null>(null);
  const [consoleVMID, setConsoleVMID] = useState<string | null>(null);

  const cluster = useCluster();
  const currentPage = PAGES.find((item) => item.id === page) ?? PAGES[0];

  useEffect(() => {
    document.documentElement.classList.toggle('dark', theme === 'dark');
    window.localStorage.setItem('compute-theme', theme);
    document
      .querySelector('meta[name="theme-color"]')
      ?.setAttribute('content', theme === 'dark' ? '#060606' : '#fefefe');
  }, [theme]);

  useEffect(() => {
    document.title = `Compute — ${currentPage.label}`;
  }, [currentPage.label]);

  useEffect(() => {
    function onHashChange() {
      const nextRoute = parseRoute();
      setRoute(nextRoute);
      if (nextRoute.page !== 'terminal') {
        setPage(nextRoute.page);
      }
    }
    window.addEventListener('hashchange', onHashChange);
    return () => window.removeEventListener('hashchange', onHashChange);
  }, []);

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        setCommandOpen((prev) => !prev);
      }
    }
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, []);

  const navigate = useCallback((next: PageId) => {
    setPage(next);
    setMobileNavOpen(false);
    window.location.hash = `#/${next}`;
  }, []);

  useEffect(() => {
    if (cluster.vms.length === 0) {
      setConsoleVMID(null);
      return;
    }
    if (!consoleVMID || !cluster.vms.some((vm) => vm.id === consoleVMID)) {
      setConsoleVMID(
        cluster.vms.find((vm) => vm.status === 'running')?.id ??
          cluster.vms[0]?.id ??
          null,
      );
    }
  }, [cluster.vms, consoleVMID]);

  const openConsole = useCallback(
    (vm: VM) => {
      setConsoleVMID(vm.id);
      navigate('console');
    },
    [navigate],
  );

  async function startVM(vm: VM) {
    const toastID = toast.loading(`Starting ${vm.name}…`);
    try {
      await api.runVM(vm.id);
      toast.success(`${vm.name} started`, { id: toastID });
      await cluster.refresh(true);
    } catch (error) {
      toast.error(`Could not start ${vm.name}`, {
        id: toastID,
        description: errorMessage(error),
      });
    }
  }

  async function stopVM(vm: VM) {
    const toastID = toast.loading(`Stopping ${vm.name}…`);
    try {
      await api.stopVM(vm.id);
      toast.success(`${vm.name} stopped`, { id: toastID });
      await cluster.refresh(true);
    } catch (error) {
      toast.error(`Could not stop ${vm.name}`, {
        id: toastID,
        description: errorMessage(error),
      });
    }
  }

  async function snapshotVM(vm: VM) {
    const toastID = toast.loading(`Snapshotting ${vm.name}…`);
    try {
      await api.snapshotVM(vm.id);
      toast.success(`${vm.name} snapshotted`, {
        id: toastID,
        description: 'Guest memory state was saved to disk.',
      });
      await cluster.refresh(true);
    } catch (error) {
      toast.error(`Could not snapshot ${vm.name}`, {
        id: toastID,
        description: errorMessage(error),
      });
    }
  }

  async function resumeVM(vm: VM) {
    const toastID = toast.loading(`Resuming ${vm.name}…`);
    try {
      await api.resumeVM(vm.id);
      toast.success(`${vm.name} resumed`, { id: toastID });
      await cluster.refresh(true);
    } catch (error) {
      toast.error(`Could not resume ${vm.name}`, {
        id: toastID,
        description: errorMessage(error),
      });
    }
  }

  async function createVM(
    payload: CreateVMRequest,
    autoStart: boolean,
    options?: { setupScript?: string },
  ) {
    const toastID = toast.loading(`Provisioning ${payload.name}…`);
    try {
      const created = await api.createVM(payload);
      toast.success(`${created.name} provisioned`, {
        id: toastID,
        description: created.ip_address
          ? `Address ${created.ip_address}`
          : 'Address assigned automatically.',
      });

      if (autoStart) {
        const bootID = toast.loading(`Booting ${created.name}…`);
        try {
          await api.runVM(created.id);
          toast.success(`${created.name} is running`, { id: bootID });
          setConsoleVMID(created.id);
        } catch (error) {
          toast.error(`Could not boot ${created.name}`, {
            id: bootID,
            description: errorMessage(error),
          });
        }
      }

      const script = options?.setupScript?.trim();
      if (script && autoStart) {
        const scriptID = toast.loading(`Running setup script in ${created.name}…`);
        try {
          const res = await api.execVM(created.id, {
            command: script,
            timeout_seconds: 300,
          });
          if (res.exit_code === 0) {
            toast.success('Setup script finished', { id: scriptID });
          } else {
            toast.error('Setup script failed', {
              id: scriptID,
              description: (res.stderr || `Exit code ${res.exit_code}`).slice(0, 240),
            });
          }
        } catch (error) {
          toast.error('Setup script failed', {
            id: scriptID,
            description: errorMessage(error),
          });
        }
      }

      await cluster.refresh(true);
    } catch (error) {
      toast.error(`Could not provision ${payload.name}`, {
        id: toastID,
        description: errorMessage(error),
      });
      throw error;
    }
  }

  async function deleteVM(id: string) {
    const toastID = toast.loading(`Deleting ${id}…`);
    try {
      await api.deleteVM(id);
      toast.success('Sandbox deleted', {
        id: toastID,
        description: 'Process, network lease, and overlay disk removed.',
      });
      if (consoleVMID === id) setConsoleVMID(null);
      if (inspectTarget?.id === id) setInspectTarget(null);
      await cluster.refresh(true);
    } catch (error) {
      toast.error('Could not delete sandbox', {
        id: toastID,
        description: errorMessage(error),
      });
      throw error;
    }
  }

  async function bulkDeleteVMs(ids: string[]) {
    const toastID = toast.loading(`Deleting ${ids.length} sandbox(es)…`);
    let deletedCount = 0;
    try {
      for (const id of ids) {
        await api.deleteVM(id);
        deletedCount++;
        if (consoleVMID === id) setConsoleVMID(null);
        if (inspectTarget?.id === id) setInspectTarget(null);
      }
      toast.success(`Deleted ${deletedCount} sandbox(es)`, {
        id: toastID,
        description: 'Processes, network leases, and overlay disks removed.',
      });
      await cluster.refresh(true);
    } catch (error) {
      toast.error('Failed to delete sandboxes', {
        id: toastID,
        description: errorMessage(error),
      });
      await cluster.refresh(true);
      throw error;
    }
  }

  const openNew = useCallback(() => setCreateOpen(true), []);

  const quickCreate = useCallback(() => {
    const name = `sbx-default-${Math.random().toString(36).slice(2, 6)}`;
    void createVM(
      {
        name,
        vcpus: cluster.config?.defaults?.vcpus || 2,
        memory_mb: cluster.config?.defaults?.memory_mb || 2048,
        public_ip_enabled: true,
      },
      true,
    );
  }, [cluster.config]);

  const pageContent = (() => {
    switch (page) {
      case 'overview':
        return (
          <DashboardPage
            stats={cluster.stats}
            vms={cluster.vms}
            workers={cluster.workers}
            events={cluster.events}
            queue={cluster.queue}
            networkConfig={cluster.networkConfig}
            isLoading={cluster.isLoading}
            onNew={openNew}
            onViewInstances={() => navigate('instances')}
            onViewNetwork={() => navigate('network')}
            onNavigate={navigate}
            onOpenConsole={openConsole}
            onStop={stopVM}
          />
        );
      case 'instances':
        return (
          <InstancesPage
            vms={cluster.vms}
            events={cluster.events}
            isLoading={cluster.isLoading}
            onNew={openNew}
            onQuickCreate={quickCreate}
            onRun={startVM}
            onStop={stopVM}
            onSnapshot={snapshotVM}
            onResume={resumeVM}
            onDelete={setDeleteTarget}
            onBulkDelete={bulkDeleteVMs}
            onInspect={setInspectTarget}
            onConsole={openConsole}
          />
        );
      case 'workers':
        return (
          <WorkersPage
            workers={cluster.workers}
            stats={cluster.stats}
            vms={cluster.vms}
            onRefresh={() => void cluster.refresh(true)}
          />
        );
      case 'jobs':
        return (
          <JobsPage
            jobs={cluster.jobs}
            queue={cluster.queue}
            onRefresh={() => void cluster.refresh(true)}
          />
        );
      case 'images':
        return (
          <ImagesTemplatesPage
            vms={cluster.vms}
            mode="snapshots"
            onLaunchVM={openNew}
            onNavigate={navigate}
            onRefreshCluster={() => void cluster.refresh(true)}
          />
        );
      case 'environment':
        return (
          <ImagesTemplatesPage
            vms={cluster.vms}
            mode="environment"
            onLaunchVM={openNew}
            onNavigate={navigate}
            onRefreshCluster={() => void cluster.refresh(true)}
          />
        );
      case 'console':
        return (
          <ConsolePage
            vms={cluster.vms}
            selectedVmID={consoleVMID}
            onSelectVM={setConsoleVMID}
            onNew={openNew}
          />
        );
      case 'network':
        return (
          <NetworkPage
            allocations={cluster.allocations}
            networkConfig={cluster.networkConfig}
            onRefresh={() => void cluster.refresh()}
          />
        );
      case 'storage':
        return (
          <StoragePage
            volumes={cluster.volumes}
            vms={cluster.vms}
            config={cluster.config}
            onRefresh={() => void cluster.refresh(true)}
          />
        );
      case 'access':
        return <AccessPage />;
      case 'events':
        return (
          <EventsPage
            events={cluster.events}
            onRefresh={() => void cluster.refresh(true)}
          />
        );
      case 'api':
        return <ApiPage config={cluster.config} />;
      case 'settings':
        return (
          <SettingsPage
            theme={theme}
            onThemeChange={setTheme}
            daemonOnline={cluster.daemonOnline}
            latency={cluster.latency}
            wsConnected={cluster.wsConnected}
            config={cluster.config}
            stats={cluster.stats}
            vms={cluster.vms}
            volumes={cluster.volumes}
          />
        );
      default:
        return null;
    }
  })();

  if (route.page === 'terminal' && route.vmId) {
    return (
      <TooltipProvider delayDuration={250}>
        <SSHTerminalWindow
          vmId={route.vmId}
          vms={cluster.vms}
          theme={theme}
          onToggleTheme={() => setTheme((t) => (t === 'light' ? 'dark' : 'light'))}
        />
        <Toaster position="bottom-right" richColors />
      </TooltipProvider>
    );
  }

  return (
    <TooltipProvider delayDuration={250}>
      <div className="min-h-screen bg-background text-foreground flex flex-col">
        <aside className="fixed inset-y-0 left-0 z-40 hidden w-56 border-r border-border lg:block">
          <Sidebar
            page={page}
            onNavigate={navigate}
            stats={cluster.stats}
            workers={cluster.workers}
            daemonOnline={cluster.daemonOnline}
            theme={theme}
            onToggleTheme={() => setTheme((t) => (t === 'light' ? 'dark' : 'light'))}
            onSetTheme={setTheme}
            onOpenCommandPalette={() => setCommandOpen(true)}
          />
        </aside>

        <Sheet open={mobileNavOpen} onOpenChange={setMobileNavOpen}>
          <SheetContent side="left" className="w-56 p-0 border-r border-border">
            <Sidebar
              page={page}
              onNavigate={navigate}
              stats={cluster.stats}
              workers={cluster.workers}
              daemonOnline={cluster.daemonOnline}
              theme={theme}
              onToggleTheme={() => setTheme((t) => (t === 'light' ? 'dark' : 'light'))}
              onSetTheme={setTheme}
              onOpenCommandPalette={() => setCommandOpen(true)}
            />
          </SheetContent>
        </Sheet>

        <div className="flex flex-1 flex-col lg:pl-56">
          <div className="lg:hidden">
            <Topbar
              page={currentPage}
              onOpenMenu={() => setMobileNavOpen(true)}
              onNew={openNew}
              onOpenKeys={() => setKeysOpen(true)}
              onNavigate={navigate}
              onOpenCommandPalette={() => setCommandOpen(true)}
              daemonOnline={cluster.daemonOnline}
              latency={cluster.latency}
              wsConnected={cluster.wsConnected}
            />
          </div>

          <main className="flex-1 px-6 py-7 sm:px-10 sm:py-8">
            <div className="mx-auto w-full max-w-[1400px]">
              {pageContent}
            </div>
          </main>
        </div>

        <CreateVMDialog
          open={createOpen}
          onOpenChange={setCreateOpen}
          config={cluster.config}
          onCreate={createVM}
        />

        <CommandPalette
          open={commandOpen}
          onOpenChange={setCommandOpen}
          onNavigate={navigate}
          onNewSandbox={openNew}
          onOpenKeys={() => setKeysOpen(true)}
          onRefresh={() => void cluster.refresh()}
          vms={cluster.vms}
        />

        <SSHKeysDialog
          open={keysOpen}
          onOpenChange={setKeysOpen}
        />

        <InspectVMDialog
          vm={inspectTarget}
          open={inspectTarget !== null}
          onOpenChange={(open) => !open && setInspectTarget(null)}
        />

        <DeleteVMDialog
          vm={deleteTarget}
          open={deleteTarget !== null}
          onOpenChange={(open) => !open && setDeleteTarget(null)}
          onConfirm={(id) => deleteVM(id)}
        />

        <Toaster position="bottom-right" richColors />
      </div>
    </TooltipProvider>
  );
}
