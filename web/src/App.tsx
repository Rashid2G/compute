import { useCallback, useEffect, useState } from 'react';
import { Loader2 } from 'lucide-react';
import { Toaster, toast } from 'sonner';

import { CommandPalette } from '@/components/command-palette';
import { CreateVMDialog } from '@/components/create-vm-dialog';
import { DeleteVMDialog } from '@/components/delete-vm-dialog';
import { InspectVMDialog } from '@/components/inspect-vm-dialog';
import { LoginDialog } from '@/components/login-dialog';
import { Sidebar } from '@/components/layout/sidebar';
import { Topbar } from '@/components/layout/topbar';
import { SSHKeysDialog } from '@/components/ssh-keys-dialog';
import { Sheet, SheetContent } from '@/components/ui/sheet';
import { TooltipProvider } from '@/components/ui/tooltip';
import { useCluster } from '@/hooks/use-cluster';
import { api, errorMessage, onUnauthorized } from '@/lib/api';
import { PAGES, resolvePageId, type PageId } from '@/lib/navigation';
import type { AuthUser, CreateVMRequest, VM } from '@/lib/types';
import { AccessPage } from '@/pages/access';
import { ApiPage } from '@/pages/api';
import { AuthPage } from '@/pages/auth';
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
type RoutePage = PageId | 'terminal' | 'login' | 'signup';

function initialTheme(): Theme {
  const stored = window.localStorage.getItem('compute-theme');
  if (stored === 'light' || stored === 'dark') return stored;
  return 'dark';
}

function parseRoute(): { page: RoutePage; vmId: string | null } {
  const rawHash = window.location.hash.replace(/^#\/?/, '');
  const clean = rawHash.split('?')[0].toLowerCase();
  if (clean === 'login') {
    return { page: 'login', vmId: null };
  }
  if (clean === 'signup') {
    return { page: 'signup', vmId: null };
  }
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
  if (r.page === 'terminal' || r.page === 'login' || r.page === 'signup') {
    return 'instances';
  }
  return r.page;
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
  const [loginOpen, setLoginOpen] = useState(false);

  const [authChecked, setAuthChecked] = useState(false);
  const [authEnabled, setAuthEnabled] = useState(true);
  const [signupEnabled, setSignupEnabled] = useState(false);
  const [authUser, setAuthUser] = useState<AuthUser | null>(null);

  const isAuthenticated = authChecked && (!authEnabled || authUser !== null);
  const cluster = useCluster({ enabled: isAuthenticated });
  const currentPage = PAGES.find((item) => item.id === page) ?? PAGES[0];

  useEffect(() => {
    return onUnauthorized(() => {
      if (!authEnabled) return;
      api.clearAuth();
      setAuthUser(null);
      if (window.location.hash !== '#/login' && window.location.hash !== '#/signup') {
        window.location.hash = '#/login';
      }
    });
  }, [authEnabled]);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      let resolvedAuthEnabled = true;
      try {
        const status = await api.authStatus();
        if (cancelled) return;
        resolvedAuthEnabled = status.auth_enabled;
        setAuthEnabled(status.auth_enabled);
        setSignupEnabled(status.signup_enabled);
      } catch {
        // Older mock servers or endpoints without /auth/status fall back to authEnabled=true
      }

      if (!resolvedAuthEnabled) {
        if (!cancelled) {
          setAuthChecked(true);
        }
        return;
      }

      try {
        const me = await api.me();
        if (!cancelled) {
          setAuthUser(me.user);
        }
      } catch {
        if (!cancelled) {
          api.clearAuth();
          setAuthUser(null);
        }
      } finally {
        if (!cancelled) {
          setAuthChecked(true);
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, []);

  const handleLogout = useCallback(async () => {
    await api.logout();
    setAuthUser(null);
    window.location.hash = '#/login';
    toast.info('Signed out');
  }, []);

  useEffect(() => {
    document.documentElement.classList.toggle('dark', theme === 'dark');
    window.localStorage.setItem('compute-theme', theme);
    document
      .querySelector('meta[name="theme-color"]')
      ?.setAttribute('content', theme === 'dark' ? '#0a0a0b' : '#fafaf9');
  }, [theme]);

  useEffect(() => {
    if (authChecked && authEnabled && authUser === null) {
      document.title = route.page === 'signup' ? 'Compute — Create Account' : 'Compute — Sign In';
      return;
    }
    document.title = `Compute — ${currentPage.label}`;
  }, [authChecked, authEnabled, authUser, currentPage.label, route.page]);

  useEffect(() => {
    function onHashChange() {
      const nextRoute = parseRoute();
      setRoute(nextRoute);
      if (nextRoute.page !== 'terminal' && nextRoute.page !== 'login' && nextRoute.page !== 'signup') {
        setPage(nextRoute.page);
      }
    }
    window.addEventListener('hashchange', onHashChange);
    return () => window.removeEventListener('hashchange', onHashChange);
  }, []);

  useEffect(() => {
    if (!authChecked) return;
    if (authEnabled && authUser === null) {
      if (route.page !== 'login' && route.page !== 'signup') {
        window.location.hash = '#/login';
      }
    } else if (route.page === 'login' || route.page === 'signup') {
      window.location.hash = '#/instances';
      setPage('instances');
    }
  }, [authChecked, authEnabled, authUser, route.page]);

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if (!isAuthenticated) return;
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        setCommandOpen((prev) => !prev);
      }
    }
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [isAuthenticated]);

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
    const vmName = vm.name || vm.id;
    const toastID = toast.loading(`Starting ${vmName}…`);
    try {
      await api.runVM(vm.id);
      toast.success(`${vmName} started`, { id: toastID });
      await cluster.refresh(true);
    } catch (error) {
      toast.error(`Could not start ${vmName}`, {
        id: toastID,
        description: errorMessage(error),
      });
    }
  }

  async function stopVM(vm: VM) {
    const vmName = vm.name || vm.id;
    const toastID = toast.loading(`Stopping ${vmName}…`);
    try {
      await api.stopVM(vm.id);
      toast.success(`${vmName} stopped`, { id: toastID });
      await cluster.refresh(true);
    } catch (error) {
      toast.error(`Could not stop ${vmName}`, {
        id: toastID,
        description: errorMessage(error),
      });
    }
  }

  async function snapshotVM(vm: VM) {
    const vmName = vm.name || vm.id;
    const toastID = toast.loading(`Snapshotting ${vmName}…`);
    try {
      await api.snapshotVM(vm.id);
      toast.success(`${vmName} snapshotted`, {
        id: toastID,
        description: 'Guest memory state was saved to disk.',
      });
      await cluster.refresh(true);
    } catch (error) {
      toast.error(`Could not snapshot ${vmName}`, {
        id: toastID,
        description: errorMessage(error),
      });
    }
  }

  async function resumeVM(vm: VM) {
    const vmName = vm.name || vm.id;
    const toastID = toast.loading(`Resuming ${vmName}…`);
    try {
      await api.resumeVM(vm.id);
      toast.success(`${vmName} resumed`, { id: toastID });
      await cluster.refresh(true);
    } catch (error) {
      toast.error(`Could not resume ${vmName}`, {
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
    const targetName = payload.name?.trim() || 'sandbox';
    const toastID = toast.loading(`Provisioning ${targetName}…`);
    try {
      const created = await api.createVM(payload);
      const createdName = created?.name || targetName;
      toast.success(`${createdName} provisioned`, {
        id: toastID,
        description: created.ip_address
          ? `Address ${created.ip_address}`
          : 'Address assigned automatically.',
      });
      void cluster.refresh(true);

      if (autoStart) {
        const bootID = toast.loading(`Booting ${createdName}…`);
        try {
          await api.runVM(created.id);
          toast.success(`${createdName} is running`, { id: bootID });
          setConsoleVMID(created.id);
          void cluster.refresh(true);
        } catch (error) {
          toast.error(`Could not boot ${createdName}`, {
            id: bootID,
            description: errorMessage(error),
          });
        }
      }

      const script = options?.setupScript?.trim();
      if (script && autoStart) {
        const scriptID = toast.loading(`Running setup script in ${createdName}…`);
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
      toast.error(`Could not provision ${targetName}`, {
        id: toastID,
        description: errorMessage(error),
      });
      throw error;
    }
  }


  async function deleteVM(id: string) {
    const vmName = cluster.vms.find((v) => v.id === id)?.name || id;
    const toastID = toast.loading(`Deleting ${vmName}…`);
    try {
      await api.deleteVM(id);
      toast.success(`${vmName} deleted`, {
        id: toastID,
        description: 'Process, network lease, and overlay disk removed.',
      });
      if (consoleVMID === id) setConsoleVMID(null);
      if (inspectTarget?.id === id) setInspectTarget(null);
      await cluster.refresh(true);
    } catch (error) {
      toast.error(`Could not delete ${vmName}`, {
        id: toastID,
        description: errorMessage(error),
      });
      throw error;
    }
  }

  async function bulkDeleteVMs(ids: string[]) {
    const toastID = toast.loading(`Deleting ${ids.length} sandbox(es)…`);
    try {
      await Promise.all(
        ids.map(async (id) => {
          await api.deleteVM(id);
          if (consoleVMID === id) setConsoleVMID(null);
          if (inspectTarget?.id === id) setInspectTarget(null);
        })
      );
      toast.success(`Deleted ${ids.length} sandbox(es)`, {
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
    void createVM(
      {
        vcpus: cluster.config?.defaults?.vcpus || 2,
        memory_mb: cluster.config?.defaults?.memory_mb || 2048,
        public_ip_enabled: true,
        network_isolation: 'account',
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
            onRefresh={() => void cluster.refresh(true)}
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
        return <AccessPage currentUser={authUser} />;
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

  if (!authChecked) {
    return (
      <TooltipProvider delayDuration={250}>
        <div className="min-h-screen bg-background text-foreground flex flex-col items-center justify-center gap-3">
          <div className="flex items-center gap-2">
            <svg
              viewBox="0 0 32 32"
              className="h-6 w-6 text-foreground"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
              aria-hidden="true"
            >
              <polygon points="16,4 27,10.5 27,21.5 16,28 5,21.5 5,10.5" />
              <polyline points="5,10.5 16,17 27,10.5" />
              <line x1="16" y1="17" x2="16" y2="28" />
            </svg>
            <span className="font-display text-xl font-medium tracking-tight">
              Compute
            </span>
          </div>
          <div className="flex items-center gap-2 text-xs text-muted-foreground font-mono">
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
            <span>Verifying session…</span>
          </div>
        </div>
        <Toaster position="bottom-right" richColors />
      </TooltipProvider>
    );
  }

  if (authEnabled && authUser === null) {
    return (
      <TooltipProvider delayDuration={250}>
        <AuthPage
          mode={route.page === 'signup' ? 'signup' : 'login'}
          signupEnabled={signupEnabled}
          theme={theme}
          onToggleTheme={() => setTheme((t) => (t === 'light' ? 'dark' : 'light'))}
          onModeChange={(nextMode) => {
            window.location.hash = `#/${nextMode}`;
          }}
          onAuthenticated={(user) => {
            setAuthUser(user);
            setMobileNavOpen(false);
            window.location.hash = '#/instances';
            setPage('instances');
          }}
        />
        <Toaster position="bottom-right" richColors />
      </TooltipProvider>
    );
  }

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
            user={authUser}
            onOpenLogin={() => setLoginOpen(true)}
            onLogout={handleLogout}
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
              user={authUser}
              onOpenLogin={() => setLoginOpen(true)}
              onLogout={handleLogout}
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
              user={authUser}
              onOpenLogin={() => setLoginOpen(true)}
              onLogout={handleLogout}
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
          onUpdated={() => void cluster.refresh(true)}
        />

        <DeleteVMDialog
          vm={deleteTarget}
          open={deleteTarget !== null}
          onOpenChange={(open) => !open && setDeleteTarget(null)}
          onConfirm={(id) => deleteVM(id)}
        />

        <LoginDialog
          open={loginOpen}
          onOpenChange={setLoginOpen}
          onSuccess={() => {
            setAuthUser(api.getAuthUser());
            void cluster.refresh(true);
          }}
        />

        <Toaster position="bottom-right" richColors />
      </div>
    </TooltipProvider>
  );
}
