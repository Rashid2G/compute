import type { ImageDraft, JobRecord, Template } from '@/lib/types';

const STORAGE_KEY = 'compute:image-drafts:v1';

export function getStoredDrafts(): ImageDraft[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw);
    if (Array.isArray(parsed)) {
      return parsed;
    }
    return [];
  } catch {
    return [];
  }
}

export function saveStoredDraft(draft: Partial<ImageDraft> & { name: string }): ImageDraft {
  const drafts = getStoredDrafts();
  const cleanName = draft.name.trim().toLowerCase();
  const id = draft.id || cleanName;
  const now = new Date().toISOString();

  const existingIndex = drafts.findIndex((d) => d.id === id || d.name.toLowerCase() === cleanName);

  const updatedDraft: ImageDraft = {
    id,
    name: cleanName,
    description: draft.description !== undefined ? draft.description : (existingIndex >= 0 ? drafts[existingIndex].description : ''),
    base_image: draft.base_image || (existingIndex >= 0 ? drafts[existingIndex].base_image : 'ubuntu-24'),
    packages: draft.packages ? [...draft.packages] : (existingIndex >= 0 ? drafts[existingIndex].packages : ['curl', 'git']),
    script: draft.script !== undefined ? draft.script : (existingIndex >= 0 ? drafts[existingIndex].script : ''),
    vcpus: draft.vcpus || (existingIndex >= 0 ? drafts[existingIndex].vcpus : 2),
    memory_mb: draft.memory_mb || (existingIndex >= 0 ? drafts[existingIndex].memory_mb : 2048),
    disk_mb: draft.disk_mb || (existingIndex >= 0 ? drafts[existingIndex].disk_mb : 10240),
    status: draft.status || (existingIndex >= 0 ? drafts[existingIndex].status : 'draft'),
    last_job_id: draft.last_job_id !== undefined ? draft.last_job_id : (existingIndex >= 0 ? drafts[existingIndex].last_job_id : undefined),
    last_error: draft.last_error !== undefined ? draft.last_error : (existingIndex >= 0 ? drafts[existingIndex].last_error : undefined),
    last_exit_code: draft.last_exit_code !== undefined ? draft.last_exit_code : (existingIndex >= 0 ? drafts[existingIndex].last_exit_code : undefined),
    template_id: draft.template_id !== undefined ? draft.template_id : (existingIndex >= 0 ? drafts[existingIndex].template_id : undefined),
    template_name: draft.template_name !== undefined ? draft.template_name : (existingIndex >= 0 ? drafts[existingIndex].template_name : undefined),
    created_at: existingIndex >= 0 ? drafts[existingIndex].created_at : (draft.created_at || now),
    updated_at: now,
  };

  if (existingIndex >= 0) {
    drafts[existingIndex] = updatedDraft;
  } else {
    drafts.unshift(updatedDraft);
  }

  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(drafts));
  } catch {
    // ignore quota error
  }

  return updatedDraft;
}

export function deleteStoredDraft(idOrName: string): ImageDraft[] {
  const drafts = getStoredDrafts().filter(
    (d) => d.id !== idOrName && d.name.toLowerCase() !== idOrName.toLowerCase(),
  );
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(drafts));
  } catch {
    // ignore
  }
  return drafts;
}

export function reconcileDraftsWithJobsAndTemplates(
  storedDrafts: ImageDraft[],
  jobs: JobRecord[],
  templates: Template[],
): ImageDraft[] {
  const map = new Map<string, ImageDraft>();

  // 1. Add all stored drafts
  for (const draft of storedDrafts) {
    map.set(draft.name.toLowerCase(), { ...draft });
  }

  // 2. Synthesize or update from jobs
  // Filter for image build jobs sorted by created_at descending
  const imageJobs = (jobs || []).filter((j) => {
    if (j.type === 'image-build' || j.type === 'image.build' || j.source === 'image-builder') {
      return true;
    }
    try {
      const spec = JSON.parse(j.spec_json || '{}');
      return Boolean(spec.target_template);
    } catch {
      return false;
    }
  });

  for (const j of imageJobs) {
    let spec: Record<string, any> = {};
    try {
      spec = JSON.parse(j.spec_json || '{}');
    } catch {
      // ignore
    }
    const targetName = (spec.target_template || j.repo_name || '').trim().toLowerCase();
    if (!targetName) continue;

    let result: Record<string, any> = {};
    try {
      result = JSON.parse(j.result_json || '{}');
    } catch {
      // ignore
    }

    const existing = map.get(targetName);
    const isNewer =
      !existing ||
      new Date(j.created_at || j.queued_at || 0).getTime() >=
        new Date(existing.updated_at || existing.created_at || 0).getTime();

    if (existing) {
      // Update job linkage and status if job is latest
      if (isNewer || existing.status === 'building') {
        if (j.state === 'running' || j.state === 'queued' || j.state === 'assigned') {
          existing.status = 'building';
          existing.last_job_id = j.id;
        } else if (j.state === 'succeeded') {
          existing.status = 'succeeded';
          existing.last_job_id = j.id;
          existing.template_name = result.template_name || targetName;
          existing.last_error = undefined;
          existing.last_exit_code = 0;
        } else if (j.state === 'failed' || j.state === 'canceled') {
          existing.status = 'failed';
          existing.last_job_id = j.id;
          existing.last_error = result.error || 'Build exited with an error';
          existing.last_exit_code = result.exit_code !== undefined ? result.exit_code : 1;
        }
      }
    } else {
      // Synthesize draft from job spec
      let status: ImageDraft['status'] = 'draft';
      if (j.state === 'running' || j.state === 'queued' || j.state === 'assigned') {
        status = 'building';
      } else if (j.state === 'succeeded') {
        status = 'succeeded';
      } else if (j.state === 'failed' || j.state === 'canceled') {
        status = 'failed';
      }

      map.set(targetName, {
        id: targetName,
        name: targetName,
        description: spec.target_description || '',
        base_image: spec.image_id || 'ubuntu-24',
        packages: spec.packages || [],
        script: spec.script || (spec.command && spec.command.length > 2 ? spec.command[2] : ''),
        vcpus: spec.vcpus || 2,
        memory_mb: spec.memory_mb || 2048,
        disk_mb: spec.disk_mb || 10240,
        status,
        last_job_id: j.id,
        last_error: result.error,
        last_exit_code: result.exit_code,
        template_name: status === 'succeeded' ? (result.template_name || targetName) : undefined,
        created_at: j.created_at || new Date().toISOString(),
        updated_at: j.updated_at || j.created_at || new Date().toISOString(),
      });
    }
  }

  // 3. Mark templates as succeeded if corresponding draft exists
  for (const tpl of templates || []) {
    const key = tpl.name.toLowerCase();
    const existing = map.get(key);
    if (existing) {
      if (existing.status !== 'building') {
        existing.status = 'succeeded';
        existing.template_id = tpl.id;
        existing.template_name = tpl.name;
      }
    }
  }

  return Array.from(map.values()).sort(
    (a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime(),
  );
}
