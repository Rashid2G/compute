import { useEffect, useState } from 'react';
import {
  AlertCircle,
  ArrowLeft,
  Eye,
  EyeOff,
  Loader2,
  Lock,
  Moon,
  ShieldAlert,
  Sun,
  Terminal,
  UserPlus,
} from 'lucide-react';
import { toast } from 'sonner';

import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { api, errorMessage } from '@/lib/api';
import type { AuthUser } from '@/lib/types';
import { cn } from '@/lib/utils';

const DNS_LABEL_REGEX = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;

interface AuthPageProps {
  mode: 'login' | 'signup';
  signupEnabled: boolean;
  theme: 'light' | 'dark';
  onToggleTheme: () => void;
  onModeChange: (mode: 'login' | 'signup') => void;
  onAuthenticated: (user: AuthUser) => void;
}

export function AuthPage({
  mode,
  signupEnabled,
  theme,
  onToggleTheme,
  onModeChange,
  onAuthenticated,
}: AuthPageProps) {
  const [identifier, setIdentifier] = useState('');
  const [loginPassword, setLoginPassword] = useState('');
  const [showLoginPassword, setShowLoginPassword] = useState(false);

  const [email, setEmail] = useState('');
  const [username, setUsername] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [signupPassword, setSignupPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [showSignupPassword, setShowSignupPassword] = useState(false);

  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setError(null);
  }, [mode]);

  const normalizedUsername = username.trim().toLowerCase();
  const usernameValid =
    normalizedUsername === '' || DNS_LABEL_REGEX.test(normalizedUsername);

  async function handleLoginSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!identifier.trim() || !loginPassword) {
      setError('Username or email and password are required.');
      return;
    }
    setLoading(true);
    setError(null);
    try {
      const res = await api.login(identifier.trim(), loginPassword);
      toast.success('Signed in', {
        description: `Welcome back, ${res.user.display_name || res.user.username}.`,
      });
      setLoginPassword('');
      onAuthenticated(res.user);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  async function handleSignupSubmit(e: React.FormEvent) {
    e.preventDefault();
    const cleanEmail = email.trim();
    const cleanUsername = username.trim().toLowerCase();
    const cleanDisplayName = displayName.trim();

    if (!cleanEmail || !cleanUsername || !signupPassword) {
      setError('Email, username, and password are required.');
      return;
    }
    if (!DNS_LABEL_REGEX.test(cleanUsername)) {
      setError(
        'Username must be 1–63 characters of lowercase letters, digits, or hyphens, starting and ending with a letter or digit.',
      );
      return;
    }
    if (signupPassword.length < 8) {
      setError('Password must be at least 8 characters.');
      return;
    }
    if (signupPassword !== confirmPassword) {
      setError('Passwords do not match.');
      return;
    }

    setLoading(true);
    setError(null);
    try {
      const res = await api.signup({
        email: cleanEmail,
        username: cleanUsername,
        display_name: cleanDisplayName || undefined,
        password: signupPassword,
      });
      toast.success('Account created', {
        description: `Signed in as ${res.user.display_name || res.user.username}.`,
      });
      setSignupPassword('');
      setConfirmPassword('');
      onAuthenticated(res.user);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="min-h-screen bg-background text-foreground flex flex-col justify-between">
      {/* Top header strip */}
      <header className="border-b border-border px-6 py-4 flex items-center justify-between">
        <div className="flex items-center gap-2.5">
          <svg
            viewBox="0 0 32 32"
            className="h-6 w-6 shrink-0 text-foreground"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            aria-hidden="true"
          >
            <polygon points="16,4 27,10.5 27,21.5 16,28 5,21.5 5,10.5" />
            <polyline points="5,10.5 16,17 27,10.5" />
            <line x1="16" y1="17" x2="16" y2="28" />
          </svg>
          <span className="font-display text-[21px] font-medium tracking-tight leading-none">
            Compute
          </span>
          <span className="hidden sm:inline-block ml-2 pl-2.5 border-l border-border font-mono text-[11px] uppercase tracking-wider text-muted-foreground">
            Control Plane
          </span>
        </div>

        <button
          type="button"
          onClick={onToggleTheme}
          aria-label={theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'}
          className="flex h-8 items-center gap-1.5 border border-border bg-background px-2.5 text-xs text-muted-foreground hover:text-foreground hover:bg-muted/50 transition-colors"
        >
          {theme === 'dark' ? (
            <>
              <Sun className="h-3.5 w-3.5" />
              <span>Light</span>
            </>
          ) : (
            <>
              <Moon className="h-3.5 w-3.5" />
              <span>Dark</span>
            </>
          )}
        </button>
      </header>

      {/* Center authentication card */}
      <main className="flex-1 flex items-center justify-center px-4 py-10">
        <div className="w-full max-w-[440px] space-y-6">
          <div className="space-y-1.5">
            <h1 className="font-display text-2xl font-medium tracking-tight text-foreground">
              {mode === 'signup'
                ? signupEnabled
                  ? 'Create your Compute account'
                  : 'Registration disabled'
                : 'Sign in to Compute'}
            </h1>
            <p className="text-sm text-muted-foreground">
              Self-hosted Firecracker microVM sandboxes
            </p>
          </div>

          <div className="border border-border bg-card">
            {signupEnabled && (
              <div className="grid grid-cols-2 border-b border-border bg-muted/30">
                <button
                  type="button"
                  onClick={() => onModeChange('login')}
                  className={cn(
                    'h-10 text-xs font-medium transition-colors',
                    mode === 'login'
                      ? 'bg-background text-foreground border-b-2 border-b-[#263fa9]'
                      : 'text-muted-foreground hover:text-foreground',
                  )}
                >
                  Sign In
                </button>
                <button
                  type="button"
                  onClick={() => onModeChange('signup')}
                  className={cn(
                    'h-10 text-xs font-medium border-l border-border transition-colors',
                    mode === 'signup'
                      ? 'bg-background text-foreground border-b-2 border-b-[#263fa9]'
                      : 'text-muted-foreground hover:text-foreground',
                  )}
                >
                  Create Account
                </button>
              </div>
            )}

            <div className="p-6">
              {mode === 'signup' && !signupEnabled ? (
                <div className="space-y-4">
                  <div className="flex items-start gap-3 border border-border bg-muted/40 p-4">
                    <ShieldAlert className="h-5 w-5 text-muted-foreground shrink-0 mt-0.5" />
                    <div className="space-y-1.5 text-xs">
                      <p className="font-medium text-foreground">
                        Public self-registration is disabled
                      </p>
                      <p className="text-muted-foreground leading-relaxed">
                        This Compute instance requires an administrator account or an
                        admin-provisioned user. To allow public signups, set{' '}
                        <code className="font-mono text-foreground">
                          COMPUTE_SIGNUP_ENABLED=true
                        </code>{' '}
                        in your <code className="font-mono text-foreground">.env</code>{' '}
                        configuration.
                      </p>
                    </div>
                  </div>
                  <Button
                    type="button"
                    onClick={() => onModeChange('login')}
                    className="w-full gap-2"
                  >
                    <ArrowLeft className="h-4 w-4" />
                    Back to Sign In
                  </Button>
                </div>
              ) : mode === 'signup' ? (
                <form onSubmit={handleSignupSubmit} className="space-y-4">
                  {error && (
                    <div
                      role="alert"
                      className="flex items-start gap-2 border border-destructive/40 bg-destructive/10 px-3 py-2.5 text-xs text-destructive"
                    >
                      <AlertCircle className="h-4 w-4 shrink-0 mt-0.5" />
                      <span>{error}</span>
                    </div>
                  )}

                  <div className="space-y-1.5">
                    <Label htmlFor="auth-signup-email">Email</Label>
                    <Input
                      id="auth-signup-email"
                      type="email"
                      value={email}
                      onChange={(e) => setEmail(e.target.value)}
                      placeholder="you@example.com"
                      autoComplete="email"
                      disabled={loading}
                      required
                      autoFocus
                    />
                  </div>

                  <div className="space-y-1.5">
                    <Label htmlFor="auth-signup-username">Username</Label>
                    <Input
                      id="auth-signup-username"
                      value={username}
                      onChange={(e) => setUsername(e.target.value)}
                      placeholder="alice"
                      autoComplete="username"
                      disabled={loading}
                      required
                    />
                    <p
                      className={cn(
                        'text-[11px] font-mono',
                        usernameValid ? 'text-muted-foreground' : 'text-destructive',
                      )}
                    >
                      {usernameValid
                        ? `Sandbox URL prefix: <sandbox>.${normalizedUsername || 'username'}.<domain>`
                        : 'Must be lowercase letters, digits, or hyphens (DNS label).'}
                    </p>
                  </div>

                  <div className="space-y-1.5">
                    <Label htmlFor="auth-signup-display-name">
                      Display Name{' '}
                      <span className="text-muted-foreground font-normal">
                        (optional)
                      </span>
                    </Label>
                    <Input
                      id="auth-signup-display-name"
                      value={displayName}
                      onChange={(e) => setDisplayName(e.target.value)}
                      placeholder="Alice Example"
                      autoComplete="name"
                      disabled={loading}
                    />
                  </div>

                  <div className="space-y-1.5">
                    <Label htmlFor="auth-signup-password">Password</Label>
                    <div className="relative">
                      <Input
                        id="auth-signup-password"
                        type={showSignupPassword ? 'text' : 'password'}
                        value={signupPassword}
                        onChange={(e) => setSignupPassword(e.target.value)}
                        placeholder="Minimum 8 characters"
                        autoComplete="new-password"
                        disabled={loading}
                        className="pr-9"
                        minLength={8}
                        required
                      />
                      <button
                        type="button"
                        onClick={() => setShowSignupPassword((p) => !p)}
                        aria-label={showSignupPassword ? 'Hide password' : 'Show password'}
                        className="absolute right-2.5 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                        tabIndex={-1}
                      >
                        {showSignupPassword ? (
                          <EyeOff className="h-4 w-4" />
                        ) : (
                          <Eye className="h-4 w-4" />
                        )}
                      </button>
                    </div>
                  </div>

                  <div className="space-y-1.5">
                    <Label htmlFor="auth-signup-confirm">Confirm Password</Label>
                    <Input
                      id="auth-signup-confirm"
                      type={showSignupPassword ? 'text' : 'password'}
                      value={confirmPassword}
                      onChange={(e) => setConfirmPassword(e.target.value)}
                      placeholder="Re-enter password"
                      autoComplete="new-password"
                      disabled={loading}
                      minLength={8}
                      required
                    />
                  </div>

                  <Button type="submit" disabled={loading} className="w-full gap-2">
                    {loading ? (
                      <>
                        <Loader2 className="h-4 w-4 animate-spin" />
                        Creating account…
                      </>
                    ) : (
                      <>
                        <UserPlus className="h-4 w-4" />
                        Create Account
                      </>
                    )}
                  </Button>
                </form>
              ) : (
                <form onSubmit={handleLoginSubmit} className="space-y-4">
                  {error && (
                    <div
                      role="alert"
                      className="flex items-start gap-2 border border-destructive/40 bg-destructive/10 px-3 py-2.5 text-xs text-destructive"
                    >
                      <AlertCircle className="h-4 w-4 shrink-0 mt-0.5" />
                      <span>{error}</span>
                    </div>
                  )}

                  <div className="space-y-1.5">
                    <Label htmlFor="auth-login-identifier">Username or Email</Label>
                    <Input
                      id="auth-login-identifier"
                      value={identifier}
                      onChange={(e) => setIdentifier(e.target.value)}
                      placeholder="admin"
                      autoComplete="username"
                      disabled={loading}
                      required
                      autoFocus
                    />
                  </div>

                  <div className="space-y-1.5">
                    <Label htmlFor="auth-login-password">Password</Label>
                    <div className="relative">
                      <Input
                        id="auth-login-password"
                        type={showLoginPassword ? 'text' : 'password'}
                        value={loginPassword}
                        onChange={(e) => setLoginPassword(e.target.value)}
                        placeholder="Enter your password"
                        autoComplete="current-password"
                        disabled={loading}
                        className="pr-9"
                        required
                      />
                      <button
                        type="button"
                        onClick={() => setShowLoginPassword((p) => !p)}
                        aria-label={showLoginPassword ? 'Hide password' : 'Show password'}
                        className="absolute right-2.5 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                        tabIndex={-1}
                      >
                        {showLoginPassword ? (
                          <EyeOff className="h-4 w-4" />
                        ) : (
                          <Eye className="h-4 w-4" />
                        )}
                      </button>
                    </div>
                  </div>

                  <Button type="submit" disabled={loading} className="w-full gap-2">
                    {loading ? (
                      <>
                        <Loader2 className="h-4 w-4 animate-spin" />
                        Signing in…
                      </>
                    ) : (
                      <>
                        <Lock className="h-4 w-4" />
                        Sign In
                      </>
                    )}
                  </Button>
                </form>
              )}
            </div>
          </div>

          {!signupEnabled && (
            <div className="border border-border bg-muted/30 px-4 py-3 text-[11px] text-muted-foreground space-y-1">
              <div className="flex items-center gap-1.5 font-medium text-foreground">
                <Terminal className="h-3.5 w-3.5 text-muted-foreground" />
                <span>Self-hosted administration</span>
              </div>
              <p className="leading-relaxed">
                Public signup is disabled (<code className="font-mono">COMPUTE_SIGNUP_ENABLED=false</code>).
                Administrator credentials are configured via{' '}
                <code className="font-mono">COMPUTE_ADMIN_USERNAME</code> and{' '}
                <code className="font-mono">COMPUTE_ADMIN_PASSWORD</code> in{' '}
                <code className="font-mono">.env</code>.
              </p>
            </div>
          )}
        </div>
      </main>

      <footer className="border-t border-border px-6 py-3 text-[11px] font-mono text-muted-foreground flex items-center justify-between">
        <span>COMPUTE CONTROL PLANE</span>
        <span>FIRECRACKER MICROVM PLATFORM</span>
      </footer>
    </div>
  );
}
