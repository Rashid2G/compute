import { useCallback, useEffect, useState } from 'react';
import { ArrowRight, TriangleAlert } from 'lucide-react';
import { Toaster, toast } from 'sonner';

import { CreateVMDialog } from '@/components/create-vm-dialog';
import { DeleteVMDialog } from '@/components/delete-vm-dialog';
import { InspectVMDialog } from '@/components/inspect-vm-dialog';
import { SSHKeysDialog } from '@/components/ssh-keys-dialog';
import { Sidebar } from '@/components/layout/sidebar';
import { Topbar } from '@/components/layout/topbar';
import { Button } from '@/components/ui/button';
import { Sheet, SheetContent } from '@/components/ui/sheet';
import { TooltipProvider } from '@/components/ui/tooltip';
import { useCluster } from '@/hooks/use-cluster';
import { api, errorMessage } from '@/lib/api';
import type { CreateVMRequest, VM } from '@/lib/types';
import { isPageId, PAGES, type PageId } from '@/lib/navigation';
import { ApiPage } from '@/pages/api';
import { ConsolePage } from '@/pages/console';
import { DashboardPage } from '@/pages/dashboard';
import { ImagesTemplatesPage } from '@/pages/images-templates';
import { InstancesPage } from '@/pages/instances';
import { NetworkPage } from '@/pages/network';
import { SettingsPage } from '@/pages/settings';
import { SSHTerminalWindow } from '@/pages/ssh-terminal-window';

type Theme = 'light' | 'dark';

function initialTheme(): Theme {
  const stored = window.localStorage.getItem('compute-theme');
  if (stored === 'light' || stored === 'dark') return stored;
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

function parseRoute(): { page: PageId | 'terminal'; vmId: string | null } {
  const hash = window.location.hash.replace(/^#\/?/, '');
  if (hash.startsWith('terminal/')) {
    const rawId = hash.slice('terminal/'.length).split('?')[0];
    return { page: 'terminal', vmId: decodeURIComponent(rawId) };
  }
  return { page: isPageId(hash) ? hash : 'overview', vmId: null };
}

function pageFromHash(): PageId {
  const r = parseRoute();
  return r.page === 'terminal' ? 'overview' : r.page;
}

export default function App() {
  const [theme, setTheme] = useState<Theme>(initialTheme);
  const [route, setRoute] = useState(parseRoute);
  const [page, setPage] = useState<PageId>(pageFromHash);
  const [mobileNavOpen, setMobileNavOpen] = useState(false);

  const [createOpen, setCreateOpen] = useState(false);
  const [keysOpen, setKeysOpen] = useState(false);
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
      ?.setAttribute('content', theme === 'dark' ? '#111113' : '#faf9f6');
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

  const navigate = useCallback((next: PageId) => {
    setPage(next);
    setMobileNavOpen(false);
    window.location.hash = `#/${next}`;
  }, []);

  // Keep the console selection valid as instances are created or deleted.
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
        description: 'Guest memory was written to disk.',
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

  async function createVM(payload: CreateVMRequest, autoStart: boolean) {
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
      toast.success('Instance deleted', {
        id: toastID,
        description: 'The process, network lease and overlay disk were removed.',
      });
      if (consoleVMID === id) setConsoleVMID(null);
      if (inspectTarget?.id === id) setInspectTarget(null);
      await cluster.refresh(true);
    } catch (error) {
      toast.error('Could not delete instance', {
        id: toastID,
        description: errorMessage(error),
      });
      throw error;
    }
  }

  const openNew = useCallback(() => setCreateOpen(true), []);

  const pageContent = (() => {
    switch (page) {
      case 'overview':
        return (
          <DashboardPage
            stats={cluster.stats}
            vms={cluster.vms}
            queue={cluster.queue}
            networkConfig={cluster.networkConfig}
            isLoading={cluster.isLoading}
            onNew={openNew}
            onViewInstances={() => navigate('instances')}
            onViewNetwork={() => navigate('network')}
            onOpenConsole={openConsole}
            onStop={stopVM}
          />
        );
      case 'instances':
        return (
          <InstancesPage
            vms={cluster.vms}
            isLoading={cluster.isLoading}
            onNew={openNew}
            onRun={startVM}
            onStop={stopVM}
            onSnapshot={snapshotVM}
            onResume={resumeVM}
            onDelete={setDeleteTarget}
            onInspect={setInspectTarget}
            onConsole={openConsole}
          />
        );
      case 'images':
        return (
          <ImagesTemplatesPage
            vms={cluster.vms}
            onLaunchVM={openNew}
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
      <div className="min-h-screen bg-background">
        <aside className="fixed inset-y-0 left-0 z-40 hidden w-60 border-r lg:block">
          <Sidebar
            page={page}
            onNavigate={navigate}
            stats={cluster.stats}
            daemonOnline={cluster.daemonOnline}
            theme={theme}
            onToggleTheme={() => setTheme((t) => (t === 'light' ? 'dark' : 'light'))}
          />
        </aside>

        <Sheet open={mobileNavOpen} onOpenChange={setMobileNavOpen}>
          <SheetContent side="left" className="w-64 p-0">
            <Sidebar
              page={page}
              onNavigate={navigate}
              stats={cluster.stats}
              daemonOnline={cluster.daemonOnline}
              theme={theme}
              onToggleTheme={() => setTheme((t) => (t === 'light' ? 'dark' : 'light'))}
            />
          </SheetContent>
        </Sheet>

        <div className="flex flex-col lg:pl-60">
          <Topbar
            page={currentPage}
            onOpenMenu={() => setMobileNavOpen(true)}
            onNew={openNew}
            onOpenKeys={() => setKeysOpen(true)}
            daemonOnline={cluster.daemonOnline}
            latency={cluster.latency}
            wsConnected={cluster.wsConnected}
          />

          {!cluster.daemonOnline ? (
            <div className="flex items-center justify-between border-b bg-destructive/10 px-6 py-2.5 text-xs text-destructive">
              <div className="flex items-center gap-2">
                <TriangleAlert className="h-4 w-4" />
                <span>
                  The Compute daemon could not be reached. Ensure <code className="font-mono">microforged</code> is running.
                </span>
              </div>
              <Button
                size="sm"
                variant="outline"
                className="border-destructive/30 hover:bg-destructive/15"
                onClick={() => void cluster.refresh()}
              >
                Retry
              </Button>
            </div>
          ) : null}

          <main className="flex-1 p-4 sm:p-6 lg:p-8 pb-16">{pageContent}</main>
        </div>

        <CreateVMDialog
          open={createOpen}
          onOpenChange={setCreateOpen}
          config={cluster.config}
          onCreate={createVM}
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
