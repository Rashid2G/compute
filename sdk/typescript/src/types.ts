export type NetworkIsolationMode = 'account' | 'isolated';

export interface SandboxCreateOptions {
  template?: string;
  tier?: '1vcpu' | '2vcpu' | '4vcpu' | string;
  vcpus?: number;
  memoryMb?: number;
  diskMb?: number;
  name?: string;
  apiKey?: string;
  host?: string;
  timeout?: number;
  sshPassword?: string;
  networkIsolation?: NetworkIsolationMode;
  isolated?: boolean;
}

export interface ExecutionResult {
  stdout: string;
  stderr: string;
  exitCode: number;
  durationMs: number;
}

export interface FileEntry {
  name: string;
  path: string;
  isDir: boolean;
  size: number;
}

export interface ProcessInfo {
  pid: number;
  ppid: number;
  user: string;
  cpu: number;
  memory: number;
  status: string;
  command: string;
  args: string;
}
