import { toast } from 'sonner';

import { api } from './api';

export interface QueuedTaskLabels {
  /** Past-tense verb for the success message, e.g. "Created". */
  doneVerb: string;
  /** Noun for the message, e.g. "sandboxes". */
  noun: string;
}

const TASK_POLL_INTERVAL_MS = 1500;
const TASK_POLL_TIMEOUT_MS = 15 * 60 * 1000;

function describe(items?: { id: string; error: string }[]): string | undefined {
  if (!items || items.length === 0) return undefined;
  return items.map((item) => `${item.id}: ${item.error}`).join('; ');
}

function elapsedLabel(ms?: number): string | undefined {
  if (!ms || ms < 1000) return undefined;
  const seconds = Math.round(ms / 1000);
  if (seconds < 60) return `${seconds}s elapsed`;
  const minutes = Math.floor(seconds / 60);
  return `${minutes}m ${seconds % 60}s elapsed`;
}

/**
 * Resolves a queued bulk-task acknowledgement into a final toast.
 *
 * The backend keeps executing accepted work even when the tab is refreshed or
 * disconnected, so this is best-effort UX only: if the task endpoint is
 * unavailable or the tab goes away, the "Queued…" toast simply stays.
 */
export async function resolveQueuedTask(
  taskID: string | undefined,
  toastID: string | number,
  labels: QueuedTaskLabels,
): Promise<void> {
  if (!taskID) return;
  const deadline = Date.now() + TASK_POLL_TIMEOUT_MS;
  while (Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, TASK_POLL_INTERVAL_MS));
    let task;
    try {
      task = await api.getBulkTask(taskID);
    } catch {
      return;
    }
    if (!task) return;

    if (task.status === 'queued' || task.status === 'running') {
      const done = task.completed ?? task.succeeded?.length ?? 0;
      const progress =
        task.requested > 0 ? `${done}/${task.requested}` : `${done}`;
      const stage = task.stage || `${labels.doneVerb} ${labels.noun}…`;
      toast.loading(`${stage} · ${progress}`, {
        id: toastID,
        description: elapsedLabel(task.elapsed_ms),
      });
      continue;
    }

    const done = task.succeeded?.length ?? 0;
    const warningText = describe(task.warnings);
    const failureText = describe(task.failed);
    const description = [warningText, failureText].filter(Boolean).join('; ') || undefined;

    if (task.status === 'completed') {
      toast.success(`${labels.doneVerb} ${done} ${labels.noun}`, {
        id: toastID,
        description: warningText,
      });
    } else if (task.status === 'partial') {
      toast.warning(`${labels.doneVerb} ${done} of ${task.requested} ${labels.noun}`, {
        id: toastID,
        description,
      });
    } else {
      toast.error(`${labels.doneVerb} failed`, { id: toastID, description });
    }
    return;
  }

  toast.warning(`${labels.doneVerb} is still running in the background`, {
    id: toastID,
    description: 'You can keep working; check the sandboxes list for progress.',
  });
}
