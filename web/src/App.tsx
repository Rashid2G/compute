import { useCallback, useEffect, useMemo, useState } from 'react';
import { Loader2, Menu } from 'lucide-react';
import { Toaster, toast } from 'sonner';

import { CommandPalette } from '@/components/command-palette';
import { CreateVMDialog } from '@/components/create-vm-dialog';
import { DeleteVMDialog } from '@/components/delete-vm-dialog';
import { InspectVMDialog } from '@/components/inspect-vm-dialog';
import { LoginDialog } from '@/components/login-dialog';
import { Sidebar } from '@/components/layout/sidebar';
import { SSHKeysDialog } from '@/components/ssh-keys-dialog';
import { Sheet, SheetContent } from '@/components/ui/sheet';
import { TooltipProvider } from '@/components/ui/tooltip';
import { useCluster } from '@/hooks/use-cluster';
import { api, errorMessage, onUnauthorized } from '@/lib/api';
import {
  PAGES,
  resolveRoute,
  type PageId,
  type SettingsTabId,
} from '@/lib/navigation';
import { openSSHTerminalWindow } from '@/lib/terminal';
import type { AuthUser, CreateVMRequest, VM } from '@/lib/types';
import { cn } from '@/lib/utils';
import { AuthPage } from '@/pages/auth';
import { CIPage } from '@/pages/ci';
import { ImagesTemplatesPage } from '@/pages/images-templates';
import { InstancesPage } from '@/pages/instances';
import { ResourcesPage } from '@/pages/resources';
import { SettingsPage } from '@/pages/settings';
import { SSHTerminalWindow } from '@/pages/ssh-terminal-window';
import { StoragePage } from '@/pages/storage';
import { WorkersPage } from '@/pages/workers';

type Theme = 'light' | 'dark';
type RoutePage = PageId | 'terminal' | 'login' | 'signup';

interface ParsedRoute {
  page: RoutePage;
  vmId: string | null;
  settingsTab?: SettingsTabId;
  redirectTo?: PageId;
}

function initialTheme(): Theme {
  const stored = window.localStorage.getItem('compute-theme');
  if (stored === 'light' || stored === 'dark') return stored;
  return 'dark';
}

function parseRoute(): ParsedRoute {
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
  const resolved = resolveRoute(rawHash);
  return {
    page: resolved.page,
    vmId: null,
    settingsTab: resolved.settingsTab,
    redirectTo: resolved.redirectTo,
  };
}

function pageFromRoute(r: ParsedRoute): PageId {
  if (r.page === 'terminal' || r.page === 'login' || r.page === 'signup') {
    return 'instances';
  }
  return r.page;
}

export default function App() {
  const [theme, setTheme] = useState<Theme>(initialTheme);
  const [route, setRoute] = useState<ParsedRoute>(parseRoute);
  const [page, setPage] = useState<PageId>(() => pageFromRoute(route));
  const [settingsTab, setSettingsTab] = useState<SettingsTabId>(
    () => route.settingsTab ?? 'general',
  );
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);

  const [createOpen, setCreateOpen] = useState(false);
  const [keysOpen, setKeysOpen] = useState(false);
  const [commandOpen, setCommandOpen] = useState(false);
  const [inspectTarget, setInspectTarget] = useState<VM | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<VM | null>(null);
  const [loginOpen, setLoginOpen] = useState(false);

  const [authChecked, setAuthChecked] = useState(false);
  const [authEnabled, setAuthEnabled] = useState(true);
  const [signupEnabled, setSignupEnabled] = useState(false);
  const [authUser, setAuthUser] = useState<AuthUser | null>(null);

  const isAuthenticated = authChecked && (!authEnabled || authUser !== null);
  const isAdmin = !authEnabled || authUser?.role === 'admin';
  const cluster = useCluster({ enabled: isAuthenticated });
  const visibleVMs = useMemo(
    () =>
      cluster.vms.filter(
        (v) => v.owner_id !== 'jobs' && !v.name?.startsWith('job-'),
      ),
    [cluster.vms],
  );
  const currentPage = PAGES.find((item) => item.id === page) ?? PAGES[0];

  useEffect(() => {
    return onUnauthorized(() => {
      if (!authEnabled) return;
      api.clearAuth();
      setAuthUser(null);
      if (
        window.location.hash !== '#/login' &&
        window.location.hash !== '#/signup'
      ) {
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
      document.title =
        route.page === 'signup'
          ? 'Compute — Create Account'
          : 'Compute — Sign In';
      return;
    }
    document.title = `Compute — ${currentPage.label}`;
  }, [authChecked, authEnabled, authUser, currentPage.label, route.page]);

  useEffect(() => {
    function onHashChange() {
      const nextRoute = parseRoute();
      setRoute(nextRoute);
      if (
        nextRoute.page !== 'terminal' &&
        nextRoute.page !== 'login' &&
        nextRoute.page !== 'signup'
      ) {
        setPage(nextRoute.page);
      }
      if (nextRoute.settingsTab) {
        setSettingsTab(nextRoute.settingsTab);
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
    } else if (route.redirectTo) {
      window.location.hash = `#/${route.redirectTo}`;
      setPage(route.redirectTo);
    }
  }, [authChecked, authEnabled, authUser, route.page, route.redirectTo]);

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
    if (next === 'settings') {
      setSettingsTab('general');
    }
    window.location.hash = `#/${next}`;
  }, []);

  const navigateSettingsTab = useCallback((nextTab: SettingsTabId) => {
    setPage('settings');
    setSettingsTab(nextTab);
    setMobileNavOpen(false);
    window.location.hash =
      nextTab === 'general' ? '#/settings' : `#/settings?tab=${nextTab}`;
  }, []);

  const openConsole = useCallback((vm: VM) => {
    const win = openSSHTerminalWindow(vm.id, vm.name);
    if (!win) {
      window.location.hash = `#/terminal/${encodeURIComponent(vm.id)}`;
    }
  }, []);

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

  async function createVM(payload: CreateVMRequest, autoStart: boolean) {
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
          void cluster.refresh(true);
        } catch (error) {
          toast.error(`Could not boot ${createdName}`, {
            id: bootID,
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
          if (inspectTarget?.id === id) setInspectTarget(null);
        }),
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

  const pageContent = (() => {
    switch (page) {
      case 'instances':
        return (
          <InstancesPage
            vms={visibleVMs}
            events={cluster.events}
            isLoading={cluster.isLoading}
            onNew={openNew}
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
      case 'images':
        return (
          <ImagesTemplatesPage
            vms={visibleVMs}
            mode="snapshots"
            onLaunchVM={openNew}
            onNavigate={navigate}
            onRefreshCluster={() => void cluster.refresh(true)}
          />
        );
      case 'storage':
        return (
          <StoragePage
            volumes={cluster.volumes}
            vms={visibleVMs}
            config={cluster.config}
            onRefresh={() => void cluster.refresh(true)}
          />
        );
      case 'jobs':
        return (
          <CIPage
            jobs={cluster.jobs}
            queue={cluster.queue}
            onRefresh={() => void cluster.refresh(true)}
          />
        );
      case 'workers':
        return (
          <WorkersPage
            workers={cluster.workers}
            stats={cluster.stats}
            vms={visibleVMs}
            volumes={cluster.volumes}
            config={cluster.config}
            onRefresh={() => void cluster.refresh(true)}
          />
        );
      case 'resources':
        return (
          <ResourcesPage
            stats={cluster.stats}
            vms={visibleVMs}
            workers={cluster.workers}
            volumes={cluster.volumes}
            config={cluster.config}
            currentUser={authUser}
            authEnabled={authEnabled}
          />
        );
      case 'settings':
        return (
          <SettingsPage
            activeTab={settingsTab}
            onTabChange={navigateSettingsTab}
            theme={theme}
            onThemeChange={setTheme}
            daemonOnline={cluster.daemonOnline}
            latency={cluster.latency}
            wsConnected={cluster.wsConnected}
            config={cluster.config}
            stats={cluster.stats}
            vms={visibleVMs}
            volumes={cluster.volumes}
            allocations={cluster.allocations}
            networkConfig={cluster.networkConfig}
            events={cluster.events}
            currentUser={authUser}
            onRefresh={() => void cluster.refresh(true)}
            onLaunchVM={openNew}
            onNavigate={navigate}
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
          onToggleTheme={() =>
            setTheme((t) => (t === 'light' ? 'dark' : 'light'))
          }
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
          onToggleTheme={() =>
            setTheme((t) => (t === 'light' ? 'dark' : 'light'))
          }
        />
        <Toaster position="bottom-right" richColors />
      </TooltipProvider>
    );
  }

  return (
    <TooltipProvider delayDuration={250}>
      <div className="min-h-screen bg-background text-foreground flex flex-col">
        <aside
          className={cn(
            'fixed inset-y-0 left-0 z-40 hidden border-r border-border transition-all duration-150 lg:block',
            sidebarCollapsed ? 'w-16' : 'w-56',
          )}
        >
          <Sidebar
            page={page}
            onNavigate={navigate}
            onNavigateSettingsTab={navigateSettingsTab}
            stats={cluster.stats}
            workers={cluster.workers}
            daemonOnline={cluster.daemonOnline}
            theme={theme}
            onToggleTheme={() =>
              setTheme((t) => (t === 'light' ? 'dark' : 'light'))
            }
            onSetTheme={setTheme}
            onOpenCommandPalette={() => setCommandOpen(true)}
            user={authUser}
            authEnabled={authEnabled}
            onOpenLogin={() => setLoginOpen(true)}
            onLogout={handleLogout}
            collapsed={sidebarCollapsed}
            onToggleCollapse={() => setSidebarCollapsed((c) => !c)}
          />
        </aside>

        <Sheet open={mobileNavOpen} onOpenChange={setMobileNavOpen}>
          <SheetContent side="left" className="w-56 p-0 border-r border-border">
            <Sidebar
              page={page}
              onNavigate={navigate}
              onNavigateSettingsTab={navigateSettingsTab}
              stats={cluster.stats}
              workers={cluster.workers}
              daemonOnline={cluster.daemonOnline}
              theme={theme}
              onToggleTheme={() =>
                setTheme((t) => (t === 'light' ? 'dark' : 'light'))
              }
              onSetTheme={setTheme}
              onOpenCommandPalette={() => setCommandOpen(true)}
              user={authUser}
              authEnabled={authEnabled}
              onOpenLogin={() => setLoginOpen(true)}
              onLogout={handleLogout}
            />
          </SheetContent>
        </Sheet>

        <div
          className={cn(
            'flex flex-1 flex-col transition-all duration-150',
            sidebarCollapsed ? 'lg:pl-16' : 'lg:pl-56',
          )}
        >
          <div className="flex items-center px-6 pt-5 pb-0 sm:px-10 lg:hidden">
            <button
              type="button"
              onClick={() => setMobileNavOpen(true)}
              aria-label="Open navigation"
              className="flex h-8 w-8 items-center justify-center border border-border bg-background text-foreground hover:bg-muted/50"
            >
              <Menu className="h-4 w-4" />
            </button>
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
          vms={visibleVMs}
          isAdmin={isAdmin}
        />

        <SSHKeysDialog open={keysOpen} onOpenChange={setKeysOpen} />

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
