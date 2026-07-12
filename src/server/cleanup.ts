import type { RaildropStorage } from './storage';

export interface RaildropCleanupResult {
  objectsDeleted: number;
  multipartUploadsAborted: number;
}

const expiryDayFromKey = (key: string): number | null => {
  const match = /^(?:public|private)\/tmp\/(\d+)\//.exec(key);
  if (!match) return null;
  const day = Number(match[1]);
  return Number.isSafeInteger(day) ? day : null;
};

export const cleanupExpiredObjects = async (
  storage: RaildropStorage,
  now = new Date(),
  multipartSafetyMs = 24 * 60 * 60 * 1000
): Promise<RaildropCleanupResult> => {
  const cutoffDay = Math.floor(now.getTime() / 86_400_000);
  const expiredKeys: string[] = [];
  for (const access of ['public', 'private'] as const) {
    let token: string | undefined;
    do {
      const page = await storage.list(`${access}/tmp/`, token);
      for (const object of page.objects) {
        const expiry = expiryDayFromKey(object.key);
        if (expiry === null || expiry > cutoffDay) continue;
        const stored = await storage.head(object.key);
        const exactExpiry = stored?.metadata['raildrop-expires-at'];
        if (!exactExpiry || Date.parse(exactExpiry) <= now.getTime()) expiredKeys.push(object.key);
      }
      token = page.nextToken;
    } while (token);
  }
  await storage.delete(expiredKeys);

  let multipartUploadsAborted = 0;
  for (const access of ['public', 'private'] as const) {
    const uploads = await storage.listMultipart(`${access}/`);
    for (const upload of uploads) {
      if (upload.initiated && now.getTime() - upload.initiated.getTime() < multipartSafetyMs)
        continue;
      await storage.abortMultipart(upload);
      multipartUploadsAborted += 1;
    }
  }
  return { objectsDeleted: expiredKeys.length, multipartUploadsAborted };
};

export const reconcileUntrackedObjects = async (
  storage: RaildropStorage,
  isTracked: (objects: readonly { id: string; key: string }[]) => Promise<ReadonlySet<string>>,
  options: { now?: Date; safetyMs?: number; batchSize?: number } = {}
): Promise<{ scanned: number; deleted: number }> => {
  const now = options.now ?? new Date();
  const safetyMs = options.safetyMs ?? 24 * 60 * 60 * 1000;
  const batchSize = Math.min(Math.max(options.batchSize ?? 100, 1), 500);
  let scanned = 0;
  let deleted = 0;

  for (const access of ['public', 'private'] as const) {
    let token: string | undefined;
    do {
      const page = await storage.list(`${access}/`, token);
      const candidates: { id: string; key: string }[] = [];
      for (const object of page.objects) {
        if (object.key.startsWith('public/global/')) continue;
        if (!object.lastModified || now.getTime() - object.lastModified.getTime() < safetyMs)
          continue;
        const stored = await storage.head(object.key);
        const id = stored?.metadata['raildrop-id'];
        if (id) candidates.push({ id, key: object.key });
      }
      scanned += candidates.length;
      for (let index = 0; index < candidates.length; index += batchSize) {
        const batch = candidates.slice(index, index + batchSize);
        const tracked = await isTracked(batch);
        const orphanKeys = batch
          .filter((object) => !tracked.has(object.key))
          .map((object) => object.key);
        if (orphanKeys.length > 0) {
          await storage.delete(orphanKeys);
          deleted += orphanKeys.length;
        }
      }
      token = page.nextToken;
    } while (token);
  }
  return { scanned, deleted };
};
