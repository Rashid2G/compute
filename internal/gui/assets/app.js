// MicroForge Web GUI - Application Controller
// State
const state = {
  vms: [],
  stats: null,
  networkInfo: null,
  allocations: [],
  networkConfig: null,
  currentTab: 'dashboard',
  statusFilter: 'all',
  searchQuery: '',
  refreshIntervalSec: 5,
  refreshTimer: null,
  selectedConsoleVM: '',
  activeInspectVM: null,
  vmPendingDelete: null,
  cmdHistory: [],
  cmdHistoryIndex: -1,
  isExecuting: false,
};

// Bootstrap modal instances
let createModal = null;
let inspectModal = null;
let deleteModal = null;
let toastInstance = null;

// Initialize on DOM load
document.addEventListener('DOMContentLoaded', () => {
  createModal = new bootstrap.Modal(document.getElementById('modal-create-vm'));
  inspectModal = new bootstrap.Modal(document.getElementById('modal-inspect-vm'));
  deleteModal = new bootstrap.Modal(document.getElementById('modal-delete-vm'));
  toastInstance = new bootstrap.Toast(document.getElementById('action-toast'), { delay: 3500 });

  // Initial fetch
  initialLoad();

  // Setup auto-refresh timer
  startAutoRefresh();

  // Setup keyboard shortcut for manual refresh (Ctrl+R or F5 standard, plus 'r' when not in inputs)
  document.addEventListener('keydown', (e) => {
    if (e.key === 'r' && !['INPUT', 'TEXTAREA', 'SELECT'].includes(e.target.tagName)) {
      e.preventDefault();
      manualRefresh();
    }
  });
});

async function initialLoad() {
  await Promise.all([
    checkHealth(),
    refreshAllData()
  ]);
}

// Polling and Refresh Management
function startAutoRefresh() {
  stopAutoRefresh();
  if (state.refreshIntervalSec > 0) {
    state.refreshTimer = setInterval(() => {
      // Don't auto-refresh while executing commands or modal is active
      const isModalOpen = document.body.classList.contains('modal-open');
      if (!state.isExecuting && !isModalOpen) {
        refreshAllData(true);
      }
    }, state.refreshIntervalSec * 1000);
  }
}

function stopAutoRefresh() {
  if (state.refreshTimer) {
    clearInterval(state.refreshTimer);
    state.refreshTimer = null;
  }
}

function setRefreshInterval(seconds) {
  state.refreshIntervalSec = seconds;
  const label = document.getElementById('refresh-interval-label');
  if (seconds === 0) {
    label.innerText = 'Paused';
    label.classList.add('text-warning');
  } else {
    label.innerText = `${seconds}s`;
    label.classList.remove('text-warning');
  }

  // Update active state in dropdown
  const items = document.querySelectorAll('#btn-refresh-interval + .dropdown-menu .dropdown-item');
  items.forEach(item => {
    item.classList.remove('active');
    if ((seconds === 0 && item.innerText.includes('Pause')) ||
        (seconds > 0 && item.innerText.includes(`${seconds} seconds`))) {
      item.classList.add('active');
    }
  });

  startAutoRefresh();
}

async function manualRefresh() {
  const icon = document.getElementById('refresh-icon');
  icon.classList.add('spin-animation');
  await refreshAllData(false);
  setTimeout(() => icon.classList.remove('spin-animation'), 400);
}

// API Calls & Data Fetching
async function refreshAllData(isBackground = false) {
  try {
    const [vmsRes, statsRes, netRes] = await Promise.all([
      fetch('/api/v1/vms'),
      fetch('/api/v1/stats'),
      fetch('/api/v1/network')
    ]);

    if (vmsRes.ok) state.vms = await vmsRes.json() || [];
    if (statsRes.ok) state.stats = await statsRes.json();
    if (netRes.ok) state.networkInfo = await netRes.json();

    // Render active tab views
    renderDashboard();
    renderVMsTable();
    updateConsoleDropdown();

    if (state.currentTab === 'network') {
      refreshNetworkAllocations();
    }
  } catch (err) {
    console.error('Error fetching data:', err);
    updateDaemonHealth(false, err.message);
  }
}

async function checkHealth() {
  const start = performance.now();
  try {
    const res = await fetch('/api/v1/health');
    const latency = Math.round(performance.now() - start);
    if (res.ok) {
      updateDaemonHealth(true, `${latency}ms`);
    } else {
      updateDaemonHealth(false, `HTTP ${res.status}`);
    }
  } catch (err) {
    updateDaemonHealth(false, 'Disconnected');
  }
}

function updateDaemonHealth(isOk, latencyOrErr) {
  const dot = document.getElementById('daemon-health-dot');
  const text = document.getElementById('daemon-health-text');
  const configStatus = document.getElementById('config-health-status');
  const configLatency = document.getElementById('config-latency-ms');

  if (isOk) {
    dot.className = 'status-dot running me-1';
    text.innerText = `Daemon: OK (${latencyOrErr})`;
    text.className = 'text-success';
    if (configStatus) {
      configStatus.className = 'badge bg-success';
      configStatus.innerText = 'Connected & Healthy';
    }
    if (configLatency) configLatency.innerText = latencyOrErr;
  } else {
    dot.className = 'status-dot stopped me-1';
    text.innerText = `Daemon: ${latencyOrErr}`;
    text.className = 'text-danger';
    if (configStatus) {
      configStatus.className = 'badge bg-danger';
      configStatus.innerText = `Error (${latencyOrErr})`;
    }
  }
}

// Tab Navigation
function switchTab(tabId) {
  state.currentTab = tabId;
  const tabs = ['dashboard', 'vms', 'console', 'network', 'config'];

  tabs.forEach(t => {
    const el = document.getElementById(`tab-${t}`);
    const navBtn = document.getElementById(`nav-${t}-tab`);
    if (t === tabId) {
      el.classList.remove('d-none');
      el.classList.add('active');
      navBtn.classList.add('active');
    } else {
      el.classList.add('d-none');
      el.classList.remove('active');
      navBtn.classList.remove('active');
    }
  });

  if (tabId === 'network') {
    refreshNetworkAllocations();
    refreshNetworkConfig();
  }
}

// ==================== RENDERING ====================

function renderDashboard() {
  const vms = state.vms || [];
  const stats = state.stats || {
    total_vms: vms.length,
    running_vms: vms.filter(v => v.status === 'running').length,
    snapshotted_vms: vms.filter(v => v.status === 'paused' || v.status === 'snapshotted').length,
    stopped_vms: vms.filter(v => v.status === 'stopped' || v.status === 'created').length,
    total_vcpus: vms.reduce((acc, v) => acc + (v.vcpus || 1), 0),
    total_memory_mb: vms.reduce((acc, v) => acc + (v.memory_mb || 512), 0)
  };

  // Stat badges
  document.getElementById('stat-total-vms').innerText = stats.total_vms;
  document.getElementById('stat-running-vms').innerText = stats.running_vms;
  document.getElementById('stat-snapshotted-vms').innerText = stats.snapshotted_vms;
  document.getElementById('stat-stopped-vms').innerText = stats.stopped_vms;
  document.getElementById('stat-total-vcpus').innerText = stats.total_vcpus;
  
  const memMB = stats.total_memory_mb;
  document.getElementById('stat-total-mem').innerText = memMB >= 1024 
    ? `${(memMB / 1024).toFixed(1)} GB` 
    : `${memMB} MB`;

  document.getElementById('nav-vms-count').innerText = vms.length;

  // Network overview on dashboard
  if (state.networkInfo) {
    const net = state.networkInfo;
    document.getElementById('dash-net-mode').innerText = (net.mode || 'nat').toUpperCase();
    document.getElementById('dash-bridge-name').innerText = net.bridge_name || 'mft3br0';
    document.getElementById('dash-bridge-status').innerText = net.bridge_active ? 'UP' : 'DOWN';
    document.getElementById('dash-bridge-status').className = net.bridge_active ? 'badge bg-success ms-1' : 'badge bg-danger ms-1';
    document.getElementById('dash-bridge-ip').innerText = `${net.bridge_ip || '172.16.0.1/16'} (GW: ${net.gateway_ip || '172.16.0.1'})`;
    document.getElementById('dash-subnet').innerText = `${net.subnet || '172.16.0.0/16'} (${net.netmask || '255.255.0.0'})`;
    document.getElementById('dash-nat-status').innerText = net.nat_enabled ? 'Active (MASQUERADE)' : 'Disabled';
    document.getElementById('dash-nat-status').className = net.nat_enabled ? 'badge bg-success' : 'badge bg-secondary';

    // Active TAPs list
    const taps = net.active_taps || [];
    document.getElementById('dash-active-taps-count').innerText = taps.length;
    const tapsContainer = document.getElementById('dash-active-taps-list');
    if (taps.length === 0) {
      tapsContainer.innerHTML = '<span class="text-secondary fst-italic">None active</span>';
    } else {
      tapsContainer.innerHTML = taps.map(t => `<span class="badge bg-dark border border-secondary text-info font-monospace">${t}</span>`).join(' ');
    }
  }

  // Dashboard VMs quick table
  const tbody = document.getElementById('dash-vms-table-body');
  if (vms.length === 0) {
    tbody.innerHTML = `<tr><td colspan="7" class="text-center py-4 text-secondary">No MicroVMs provisioned yet. Click "Provision VM" above to create one.</td></tr>`;
    return;
  }

  const recentVMs = vms.slice(0, 5);
  tbody.innerHTML = recentVMs.map(vm => renderVMTableRow(vm, false)).join('');
}

function renderVMsTable() {
  const tbody = document.getElementById('vms-table-body');
  if (!tbody) return;

  let filtered = [...(state.vms || [])];

  // Apply Status Filter
  if (state.statusFilter !== 'all') {
    filtered = filtered.filter(v => {
      if (state.statusFilter === 'running') return v.status === 'running';
      if (state.statusFilter === 'paused') return v.status === 'paused' || v.status === 'snapshotted';
      if (state.statusFilter === 'created') return v.status === 'created';
      if (state.statusFilter === 'stopped') return v.status === 'stopped';
      return true;
    });
  }

  // Apply Search Filter
  if (state.searchQuery) {
    const q = state.searchQuery.toLowerCase();
    filtered = filtered.filter(v => 
      (v.name && v.name.toLowerCase().includes(q)) ||
      (v.id && v.id.toLowerCase().includes(q)) ||
      (v.ip_address && v.ip_address.includes(q)) ||
      (v.tap_device && v.tap_device.toLowerCase().includes(q))
    );
  }

  if (filtered.length === 0) {
    tbody.innerHTML = `<tr><td colspan="8" class="text-center py-4 text-secondary">No MicroVMs match the current filter or search criteria.</td></tr>`;
    return;
  }

  tbody.innerHTML = filtered.map(vm => renderVMTableRow(vm, true)).join('');
}

function renderVMTableRow(vm, isFullTable = true) {
  const statusBadge = getStatusBadge(vm.status);
  const formattedDate = formatDate(vm.created_at);
  const specs = `${vm.vcpus || 1} vCPU / ${vm.memory_mb || 512}MB`;
  const ip = vm.ip_address || '<none>';
  const mac = vm.mac_address || '<none>';
  const tap = vm.tap_device || '<none>';
  const pid = vm.pid > 0 ? vm.pid : '-';

  const actionButtons = `
    <div class="btn-group btn-group-sm" role="group">
      ${vm.status === 'running' 
        ? `<button class="btn btn-outline-danger" onclick="stopVM('${vm.id}')" title="Stop MicroVM"><i class="bi bi-stop-fill"></i> Stop</button>
           <button class="btn btn-outline-info" onclick="saveStateVM('${vm.id}')" title="Snapshot RAM & CPU state"><i class="bi bi-camera"></i> Snapshot</button>
           <button class="btn btn-outline-light" onclick="openConsoleForVM('${vm.id}')" title="Execute commands in guest"><i class="bi bi-terminal"></i> Exec</button>`
        : (vm.status === 'paused' || vm.status === 'snapshotted')
          ? `<button class="btn btn-outline-success" onclick="resumeVM('${vm.id}')" title="Restore snapshot and resume execution"><i class="bi bi-play-circle"></i> Resume</button>`
          : `<button class="btn btn-outline-success" onclick="runVM('${vm.id}')" title="Start MicroVM"><i class="bi bi-play-fill"></i> Run</button>`
      }
      <button class="btn btn-outline-secondary" onclick="openInspectModal('${vm.id}')" title="Inspect network & configuration"><i class="bi bi-info-circle"></i></button>
      <button class="btn btn-outline-danger" onclick="openDeleteModal('${vm.id}')" title="Delete MicroVM"><i class="bi bi-trash"></i></button>
    </div>
  `;

  if (!isFullTable) {
    return `
      <tr>
        <td>${statusBadge}</td>
        <td>
          <div class="fw-bold text-white font-monospace">${escapeHtml(vm.name || vm.id)}</div>
          <div class="text-secondary font-monospace" style="font-size: 0.75rem;">${vm.id}</div>
        </td>
        <td class="font-monospace">${specs}</td>
        <td>
          <div class="font-monospace text-white">${ip}</div>
          <div class="text-secondary font-monospace" style="font-size: 0.75rem;">${mac}</div>
        </td>
        <td class="font-monospace text-secondary">${tap}</td>
        <td class="text-secondary" style="font-size: 0.8rem;">${formattedDate}</td>
        <td class="text-end">${actionButtons}</td>
      </tr>
    `;
  }

  return `
    <tr>
      <td>${statusBadge}</td>
      <td>
        <div class="fw-bold text-white font-monospace">${escapeHtml(vm.name || vm.id)}</div>
        <div class="d-flex align-items-center gap-1">
          <span class="text-secondary font-monospace" style="font-size: 0.75rem;">${vm.id}</span>
          <button class="btn-copy" onclick="copyText('${vm.id}')" title="Copy VM ID"><i class="bi bi-clipboard"></i></button>
        </div>
      </td>
      <td class="font-monospace">${specs}</td>
      <td>
        <div class="d-flex align-items-center gap-1">
          <span class="font-monospace text-white">${ip}</span>
          <button class="btn-copy" onclick="copyText('${ip}')" title="Copy IP"><i class="bi bi-clipboard"></i></button>
        </div>
        <div class="text-secondary font-monospace" style="font-size: 0.75rem;">${mac}</div>
      </td>
      <td class="font-monospace text-secondary">${tap}</td>
      <td class="font-monospace text-secondary" style="font-size: 0.8rem;">PID: ${pid}</td>
      <td class="text-secondary" style="font-size: 0.8rem;">${formattedDate}</td>
      <td class="text-end">${actionButtons}</td>
    </tr>
  `;
}

function getStatusBadge(status) {
  switch (status) {
    case 'running':
      return `<span class="badge bg-success-subtle text-success border border-success-subtle d-inline-flex align-items-center"><span class="status-dot running"></span> Running</span>`;
    case 'paused':
    case 'snapshotted':
      return `<span class="badge bg-info-subtle text-info border border-info-subtle d-inline-flex align-items-center"><span class="status-dot paused"></span> Paused</span>`;
    case 'created':
      return `<span class="badge bg-warning-subtle text-warning border border-warning-subtle d-inline-flex align-items-center"><span class="status-dot created"></span> Created</span>`;
    case 'stopped':
    default:
      return `<span class="badge bg-secondary-subtle text-secondary border border-secondary-subtle d-inline-flex align-items-center"><span class="status-dot stopped"></span> Stopped</span>`;
  }
}

function setVMFilter(filter, btn) {
  state.statusFilter = filter;
  const buttons = document.querySelectorAll('#vm-status-filter-group button');
  buttons.forEach(b => b.classList.remove('active'));
  btn.classList.add('active');
  renderVMsTable();
}

// Search input listener
document.addEventListener('DOMContentLoaded', () => {
  const searchInput = document.getElementById('vm-search-input');
  if (searchInput) {
    searchInput.addEventListener('input', (e) => {
      state.searchQuery = e.target.value.trim();
      renderVMsTable();
    });
  }
});

function refreshVMs() {
  refreshAllData(false);
}

// ==================== LIFECYCLE ACTIONS ====================

async function runVM(vmID) {
  showToast(`Starting MicroVM ${vmID}...`, 'info');
  try {
    const res = await fetch(`/api/v1/vms/${vmID}/run`, { method: 'POST' });
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || 'Failed to run VM');
    showToast(`MicroVM ${vmID} is now running (PID: ${data.pid})`, 'success');
    await refreshAllData();
  } catch (err) {
    showToast(`Run error: ${err.message}`, 'danger');
  }
}

async function stopVM(vmID) {
  showToast(`Stopping MicroVM ${vmID}...`, 'info');
  try {
    const res = await fetch(`/api/v1/vms/${vmID}/stop`, { method: 'POST' });
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || 'Failed to stop VM');
    showToast(`MicroVM ${vmID} stopped cleanly`, 'success');
    await refreshAllData();
  } catch (err) {
    showToast(`Stop error: ${err.message}`, 'danger');
  }
}

async function saveStateVM(vmID) {
  showToast(`Saving memory & CPU state snapshot for ${vmID}...`, 'info');
  try {
    const res = await fetch(`/api/v1/vms/${vmID}/save-state`, { method: 'POST' });
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || 'Failed to save state');
    showToast(`MicroVM ${vmID} memory snapshot created successfully`, 'success');
    await refreshAllData();
  } catch (err) {
    showToast(`Snapshot error: ${err.message}`, 'danger');
  }
}

async function resumeVM(vmID) {
  showToast(`Restoring snapshot state for ${vmID}...`, 'info');
  try {
    const res = await fetch(`/api/v1/vms/${vmID}/resume`, { method: 'POST' });
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || 'Failed to resume VM');
    showToast(`MicroVM ${vmID} resumed from snapshot (PID: ${data.pid})`, 'success');
    await refreshAllData();
  } catch (err) {
    showToast(`Resume error: ${err.message}`, 'danger');
  }
}

function openDeleteModal(vmID) {
  state.vmPendingDelete = vmID;
  document.getElementById('delete-vm-id-text').innerText = vmID;
  deleteModal.show();
}

async function confirmDeleteVM() {
  const vmID = state.vmPendingDelete;
  if (!vmID) return;

  const btn = document.getElementById('btn-confirm-delete');
  btn.disabled = true;
  btn.innerHTML = `<span class="spinner-border spinner-border-sm me-1"></span> Deleting...`;

  try {
    const res = await fetch(`/api/v1/vms/${vmID}`, { method: 'DELETE' });
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || 'Failed to delete VM');
    showToast(`MicroVM ${vmID} deleted successfully`, 'success');
    deleteModal.hide();
    await refreshAllData();
  } catch (err) {
    showToast(`Delete error: ${err.message}`, 'danger');
  } finally {
    btn.disabled = false;
    btn.innerHTML = `<i class="bi bi-trash"></i> Delete MicroVM`;
    state.vmPendingDelete = null;
  }
}

// ==================== PROVISION VM ====================

function openCreateModal() {
  document.getElementById('form-create-vm').reset();
  document.getElementById('vcpu-1').checked = true;
  document.getElementById('mem-512').checked = true;
  document.getElementById('create-vm-autostart').checked = true;
  createModal.show();
}

async function submitCreateVM(e) {
  e.preventDefault();
  const btn = document.getElementById('btn-submit-create');
  btn.disabled = true;
  btn.innerHTML = `<span class="spinner-border spinner-border-sm me-1"></span> Creating...`;

  const name = document.getElementById('create-vm-name').value.trim();
  const vcpus = parseInt(document.querySelector('input[name="create-vcpus"]:checked')?.value || '1', 10);
  const memoryMB = parseInt(document.querySelector('input[name="create-memory"]:checked')?.value || '512', 10);
  const ip = document.getElementById('create-vm-ip').value.trim();
  const mac = document.getElementById('create-vm-mac').value.trim();
  const autostart = document.getElementById('create-vm-autostart').checked;

  const payload = {
    name: name || undefined,
    vcpus: vcpus,
    memory_mb: memoryMB,
    ip_address: ip || undefined,
    mac_address: mac || undefined,
  };

  try {
    const res = await fetch('/api/v1/vms', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    });
    const created = await res.json();
    if (!res.ok) throw new Error(created.error || 'Failed to provision VM');

    showToast(`MicroVM ${created.id} provisioned with IP ${created.ip_address}`, 'success');
    createModal.hide();

    // If auto-start requested
    if (autostart) {
      showToast(`Starting MicroVM ${created.id}...`, 'info');
      const runRes = await fetch(`/api/v1/vms/${created.id}/run`, { method: 'POST' });
      if (runRes.ok) {
        showToast(`MicroVM ${created.id} is now running!`, 'success');
      }
    }

    await refreshAllData();
  } catch (err) {
    showToast(`Provisioning error: ${err.message}`, 'danger');
  } finally {
    btn.disabled = false;
    btn.innerHTML = `<i class="bi bi-plus-lg"></i> Create VM`;
  }
}

// ==================== INSPECT VM ====================

async function openInspectModal(vmID) {
  const vm = state.vms.find(v => v.id === vmID);
  if (!vm) return;
  state.activeInspectVM = vm;

  document.getElementById('inspect-title-id').innerText = vm.id;
  document.getElementById('inspect-id').innerText = vm.id;
  document.getElementById('inspect-name').innerText = vm.name || vm.id;
  document.getElementById('inspect-status').innerHTML = getStatusBadge(vm.status);
  document.getElementById('inspect-specs').innerText = `${vm.vcpus || 1} vCPUs, ${vm.memory_mb || 512}MB RAM`;
  document.getElementById('inspect-pid').innerText = vm.pid > 0 ? `${vm.pid}` : 'None (stopped)';
  document.getElementById('inspect-ip').innerText = vm.ip_address || '<none>';
  document.getElementById('inspect-mac').innerText = vm.mac_address || '<none>';
  document.getElementById('inspect-tap').innerText = vm.tap_device || '<none>';
  document.getElementById('inspect-gw').innerText = state.networkInfo?.gateway_ip || '172.16.0.1';
  document.getElementById('inspect-connectivity').innerHTML = '<span class="spinner-border spinner-border-sm text-secondary"></span> Probing port 22...';

  document.getElementById('inspect-disk').innerText = vm.disk_path || 'None';
  document.getElementById('inspect-rootfs').innerText = vm.rootfs_path || 'None';
  document.getElementById('inspect-kernel').innerText = vm.kernel_path || 'None';
  document.getElementById('inspect-sshkey').innerText = vm.ssh_key_path || 'None';
  document.getElementById('inspect-sock').innerText = vm.socket_path || 'None';

  const snapRow = document.getElementById('inspect-snap-row');
  if (vm.snapshot_path) {
    snapRow.classList.remove('d-none');
    document.getElementById('inspect-snap').innerText = `${vm.snapshot_path} / ${vm.mem_file_path}`;
  } else {
    snapRow.classList.add('d-none');
  }

  // SSH Access helper commands
  document.getElementById('inspect-cmd-cli').innerText = `mforge ssh ${vm.id}`;
  const keyArg = vm.ssh_key_path ? `-i ${vm.ssh_key_path} ` : '';
  document.getElementById('inspect-cmd-ssh').innerText = `ssh ${keyArg}mforge@${vm.ip_address || '<ip>'}`;

  // Console button state
  const consoleBtn = document.getElementById('btn-inspect-open-console');
  consoleBtn.disabled = vm.status !== 'running';

  inspectModal.show();

  // Run live network inspect probe
  try {
    const res = await fetch(`/api/v1/network/inspect/${vm.id}`);
    if (res.ok) {
      const inspectData = await res.json();
      if (inspectData.reachable) {
        document.getElementById('inspect-connectivity').innerHTML = `<span class="badge bg-success">Reachable (SSH Port 22 OPEN)</span>`;
      } else {
        document.getElementById('inspect-connectivity').innerHTML = `<span class="badge bg-danger">${escapeHtml(inspectData.connectivity || 'Unreachable')}</span>`;
      }
    } else {
      document.getElementById('inspect-connectivity').innerHTML = `<span class="text-secondary fst-italic">Probe unavailable</span>`;
    }
  } catch (err) {
    document.getElementById('inspect-connectivity').innerHTML = `<span class="text-secondary fst-italic">Probe failed</span>`;
  }
}

function openConsoleForCurrentVM() {
  if (state.activeInspectVM) {
    inspectModal.hide();
    openConsoleForVM(state.activeInspectVM.id);
  }
}

// ==================== WEB CONSOLE / EXEC ====================

function updateConsoleDropdown() {
  const select = document.getElementById('console-vm-select');
  if (!select) return;

  const currentVal = select.value || state.selectedConsoleVM;
  const runningVMs = (state.vms || []).filter(v => v.status === 'running');

  if (runningVMs.length === 0) {
    select.innerHTML = '<option value="">-- No running VMs --</option>';
    document.getElementById('console-prompt-label').innerText = 'mforge@vm:~$';
    document.getElementById('console-cli-ssh-hint').innerText = 'mforge ssh <vm_id>';
    return;
  }

  select.innerHTML = '<option value="">-- Select Running VM --</option>' + 
    runningVMs.map(v => `<option value="${v.id}">${v.name || v.id} (${v.ip_address})</option>`).join('');

  if (currentVal && runningVMs.some(v => v.id === currentVal)) {
    select.value = currentVal;
    state.selectedConsoleVM = currentVal;
    onConsoleVMChange();
  } else if (runningVMs.length > 0 && !select.value) {
    select.value = runningVMs[0].id;
    state.selectedConsoleVM = runningVMs[0].id;
    onConsoleVMChange();
  }
}

function onConsoleVMChange() {
  const select = document.getElementById('console-vm-select');
  const vmID = select.value;
  state.selectedConsoleVM = vmID;

  const vm = state.vms.find(v => v.id === vmID);
  const promptLabel = document.getElementById('console-prompt-label');
  const sshHint = document.getElementById('console-cli-ssh-hint');

  if (vm) {
    promptLabel.innerText = `mforge@${vm.name || vm.id}:~$`;
    sshHint.innerText = `mforge ssh ${vm.id}`;
  } else {
    promptLabel.innerText = 'mforge@vm:~$';
    sshHint.innerText = 'mforge ssh <vm_id>';
  }
}

function openConsoleForVM(vmID) {
  state.selectedConsoleVM = vmID;
  switchTab('console');
  const select = document.getElementById('console-vm-select');
  if (select) {
    select.value = vmID;
    onConsoleVMChange();
  }
  document.getElementById('console-command-input')?.focus();
}

function setConsoleCommand(cmd) {
  const input = document.getElementById('console-command-input');
  if (input) {
    input.value = cmd;
    input.focus();
  }
}

function handleConsoleKeyDown(e) {
  if (e.key === 'Enter') {
    e.preventDefault();
    executeConsoleCommand();
  } else if (e.key === 'ArrowUp') {
    if (state.cmdHistory.length > 0 && state.cmdHistoryIndex < state.cmdHistory.length - 1) {
      state.cmdHistoryIndex++;
      e.target.value = state.cmdHistory[state.cmdHistory.length - 1 - state.cmdHistoryIndex];
    }
  } else if (e.key === 'ArrowDown') {
    if (state.cmdHistoryIndex > 0) {
      state.cmdHistoryIndex--;
      e.target.value = state.cmdHistory[state.cmdHistory.length - 1 - state.cmdHistoryIndex];
    } else if (state.cmdHistoryIndex === 0) {
      state.cmdHistoryIndex = -1;
      e.target.value = '';
    }
  }
}

async function executeConsoleCommand() {
  const select = document.getElementById('console-vm-select');
  const vmID = select.value;
  if (!vmID) {
    showToast('Please select a running target microVM first.', 'warning');
    select.focus();
    return;
  }

  const input = document.getElementById('console-command-input');
  const command = input.value.trim();
  if (!command) return;

  const user = document.getElementById('console-user-select').value || 'mforge';
  const outBox = document.getElementById('console-output-box');
  const statusBadge = document.getElementById('console-status-badge');
  const durationBadge = document.getElementById('console-duration-badge');
  const execBtn = document.getElementById('btn-console-exec');
  const execIcon = document.getElementById('console-exec-icon');
  const execText = document.getElementById('console-exec-text');

  // Push to command history
  state.cmdHistory.push(command);
  state.cmdHistoryIndex = -1;

  state.isExecuting = true;
  execBtn.disabled = true;
  execIcon.className = 'spinner-border spinner-border-sm';
  execText.innerText = 'Running...';
  statusBadge.className = 'badge bg-warning font-monospace';
  statusBadge.innerText = 'Executing';
  durationBadge.innerText = '';

  const timestamp = new Date().toLocaleTimeString();
  const cmdHeader = `<span class="console-prompt">[${timestamp}] ${user}@${vmID}:~$</span> <strong class="text-white">${escapeHtml(command)}</strong>\n`;

  try {
    const res = await fetch(`/api/v1/vms/${vmID}/exec`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        command: command,
        user: user,
        timeout_seconds: 30
      })
    });

    const result = await res.json();
    if (!res.ok) throw new Error(result.error || 'Execution failed');

    statusBadge.className = result.exit_code === 0 ? 'badge bg-success font-monospace' : 'badge bg-danger font-monospace';
    statusBadge.innerText = `Exit ${result.exit_code}`;
    durationBadge.innerText = `${result.duration_ms}ms`;

    let outputHtml = cmdHeader;
    if (result.stdout) {
      outputHtml += `<span class="console-stdout">${escapeHtml(result.stdout)}</span>`;
    }
    if (result.stderr) {
      outputHtml += `<span class="console-stderr">${escapeHtml(result.stderr)}</span>`;
    }
    if (!result.stdout && !result.stderr) {
      outputHtml += `<span class="text-secondary fst-italic">&lt;no output, exit ${result.exit_code}&gt;</span>\n`;
    }

    if (outBox.innerText.includes('Select a running microVM')) {
      outBox.innerHTML = outputHtml;
    } else {
      outBox.innerHTML += '\n' + outputHtml;
    }
    outBox.scrollTop = outBox.scrollHeight;
  } catch (err) {
    statusBadge.className = 'badge bg-danger font-monospace';
    statusBadge.innerText = 'Error';
    outBox.innerHTML += `\n${cmdHeader}<span class="console-stderr">Error: ${escapeHtml(err.message)}</span>\n`;
    outBox.scrollTop = outBox.scrollHeight;
  } finally {
    state.isExecuting = false;
    execBtn.disabled = false;
    execIcon.className = 'bi bi-play-fill';
    execText.innerText = 'Execute';
    input.value = '';
    input.focus();
  }
}

function clearConsoleOutput() {
  document.getElementById('console-output-box').innerHTML = 'Console cleared. Ready for next command.';
  document.getElementById('console-status-badge').className = 'badge bg-secondary font-monospace';
  document.getElementById('console-status-badge').innerText = 'Idle';
  document.getElementById('console-duration-badge').innerText = '';
}

function copyConsoleOutput() {
  const text = document.getElementById('console-output-box').innerText;
  copyText(text);
}

// ==================== NETWORK TAB ====================

async function refreshNetworkAllocations() {
  const tbody = document.getElementById('net-allocations-table-body');
  const countBadge = document.getElementById('allocations-count-badge');
  try {
    const res = await fetch('/api/v1/network/allocations');
    if (!res.ok) throw new Error('Failed to fetch allocations');
    const allocs = await res.json() || [];
    state.allocations = allocs;

    countBadge.innerText = `${allocs.length} Lease${allocs.length === 1 ? '' : 's'}`;

    if (allocs.length === 0) {
      tbody.innerHTML = `<tr><td colspan="6" class="text-center py-4 text-secondary">No active network allocations found.</td></tr>`;
      return;
    }

    tbody.innerHTML = allocs.map(a => `
      <tr>
        <td class="text-white fw-bold">${a.ip_address}</td>
        <td class="text-secondary">${a.mac_address}</td>
        <td><span class="badge bg-dark border border-secondary text-info">${a.tap_device}</span></td>
        <td>${getStatusBadge(a.status)}</td>
        <td><span class="text-secondary">${a.vm_id}</span></td>
        <td>${escapeHtml(a.vm_name || a.vm_id)}</td>
      </tr>
    `).join('');
  } catch (err) {
    tbody.innerHTML = `<tr><td colspan="6" class="text-center py-4 text-danger">Error: ${escapeHtml(err.message)}</td></tr>`;
  }
}

async function refreshNetworkConfig() {
  const container = document.getElementById('net-json-config');
  try {
    const res = await fetch('/api/v1/network/config');
    if (!res.ok) throw new Error('Failed to fetch network config');
    const cfg = await res.json();
    state.networkConfig = cfg;
    container.innerText = JSON.stringify(cfg, null, 2);
  } catch (err) {
    container.innerText = `Error: ${err.message}`;
  }
}

function copyJSONConfig() {
  const text = document.getElementById('net-json-config').innerText;
  copyText(text);
}

async function pruneNetwork() {
  showToast('Reconciling host TAP interfaces and pruning orphans...', 'info');
  try {
    const res = await fetch('/api/v1/network/prune', { method: 'POST' });
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || 'Failed to prune network');

    const pruned = data.pruned_taps || [];
    if (pruned.length === 0) {
      showToast('Network is clean! No orphaned TAP devices found.', 'success');
    } else {
      showToast(`Pruned ${pruned.length} orphaned TAP interface(s): ${pruned.join(', ')}`, 'success');
    }
    await refreshAllData();
  } catch (err) {
    showToast(`Prune error: ${err.message}`, 'danger');
  }
}

// ==================== UTILITIES ====================

function showToast(message, type = 'info') {
  const toastText = document.getElementById('toast-text');
  const toastIcon = document.getElementById('toast-icon');

  toastText.innerText = message;
  switch (type) {
    case 'success':
      toastIcon.className = 'bi bi-check-circle-fill text-success fs-5';
      break;
    case 'danger':
      toastIcon.className = 'bi bi-exclamation-octagon-fill text-danger fs-5';
      break;
    case 'warning':
      toastIcon.className = 'bi bi-exclamation-triangle-fill text-warning fs-5';
      break;
    case 'info':
    default:
      toastIcon.className = 'bi bi-info-circle-fill text-info fs-5';
      break;
  }
  toastInstance.show();
}

function copyText(text) {
  if (!text) return;
  navigator.clipboard.writeText(text).then(() => {
    showToast('Copied to clipboard!', 'success');
  }).catch(() => {
    // Fallback
    const textarea = document.createElement('textarea');
    textarea.value = text;
    document.body.appendChild(textarea);
    textarea.select();
    document.execCommand('copy');
    document.body.removeChild(textarea);
    showToast('Copied to clipboard!', 'success');
  });
}

function formatDate(isoString) {
  if (!isoString) return '-';
  try {
    const d = new Date(isoString);
    return d.toLocaleString([], {
      month: 'short', day: 'numeric',
      hour: '2-digit', minute: '2-digit', second: '2-digit'
    });
  } catch {
    return isoString;
  }
}

function escapeHtml(str) {
  if (!str) return '';
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#039;');
}
