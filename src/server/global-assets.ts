import { createHash } from 'node:crypto';

import { encodeNamespace, namespace } from '../core';

import type { RaildropStorage } from './storage';

export interface GlobalAssetSource {
  alias: string;
  body: Uint8Array;
  filename: string;
  contentType: string;
  namespace: readonly string[];
}

export interface GlobalAssetManifestEntry {
  key: string;
  checksum: string;
  size: number;
  contentType: string;
  filename: string;
}

export interface GlobalAssetManifest {
  version: 1;
  generatedAt: string;
  assets: Record<string, GlobalAssetManifestEntry>;
}

export interface GlobalAssetSyncResult extends GlobalAssetManifest {
  orphanedKeys: string[];
}

export const GLOBAL_MANIFEST_KEY = 'public/global/_raildrop/manifest.json';

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

export const syncGlobalAssets = async (
  storage: RaildropStorage,
  sources: readonly GlobalAssetSource[]
): Promise<GlobalAssetSyncResult> => {
  const previousManifestObject = await storage.get(GLOBAL_MANIFEST_KEY);
  let previousManifest: unknown = null;
  if (previousManifestObject) {
    try {
      previousManifest = JSON.parse(
        await new Response(previousManifestObject.body as BodyInit).text()
      );
    } catch {
      previousManifest = null;
    }
  }
  const assets: Record<string, GlobalAssetManifestEntry> = {};
  const aliases = new Set<string>();
  for (const source of sources) {
    if (!/^[a-z0-9][a-z0-9-]{0,119}$/.test(source.alias)) {
      throw new Error(`Invalid global asset alias: ${source.alias}`);
    }
    if (aliases.has(source.alias)) throw new Error(`Duplicate global asset alias: ${source.alias}`);
    aliases.add(source.alias);
    namespace(...source.namespace);
    const checksum = createHash('sha256').update(source.body).digest('hex');
    const extension = /\.([a-z0-9]{1,12})$/i.exec(source.filename)?.[1]?.toLowerCase() ?? 'bin';
    const key = `public/global/${encodeNamespace(source.namespace)}/${checksum}/file.${extension}`;
    const existing = await storage.head(key);
    if (!existing) {
      await storage.put({
        key,
        body: source.body,
        contentType: source.contentType,
        cacheControl: 'public, max-age=31536000, immutable',
        metadata: {
          'raildrop-access': 'public',
          'raildrop-retention': 'permanent',
          'raildrop-checksum': checksum,
        },
      });
    }
    const verified = await storage.head(key);
    const verifiedBody = await storage.get(key);
    const verifiedChecksum = verifiedBody
      ? createHash('sha256')
          .update(new Uint8Array(await new Response(verifiedBody.body as BodyInit).arrayBuffer()))
          .digest('hex')
      : null;
    if (
      verified?.size !== source.body.byteLength ||
      verified.contentType !== source.contentType ||
      verified.metadata['raildrop-checksum'] !== checksum ||
      verifiedChecksum !== checksum
    ) {
      throw new Error(`Global asset verification failed for alias ${source.alias}.`);
    }
    assets[source.alias] = {
      key,
      checksum,
      size: source.body.byteLength,
      contentType: source.contentType,
      filename: source.filename,
    };
  }
  const manifest: GlobalAssetManifest = {
    version: 1,
    generatedAt: new Date().toISOString(),
    assets,
  };
  await storage.put({
    key: GLOBAL_MANIFEST_KEY,
    body: JSON.stringify(manifest),
    contentType: 'application/json',
    cacheControl: 'public, max-age=300, stale-while-revalidate=86400',
    metadata: {
      'raildrop-access': 'public',
      'raildrop-retention': 'permanent',
    },
  });
  const activeKeys = new Set(Object.values(assets).map((asset) => asset.key));
  const previousAssets: Record<string, unknown> =
    isRecord(previousManifest) && isRecord(previousManifest.assets) ? previousManifest.assets : {};
  const previousKeys = Object.values(previousAssets).flatMap((asset) =>
    isRecord(asset) && typeof asset.key === 'string' ? [asset.key] : []
  );
  const orphanedKeys = [...new Set(previousKeys)].filter((key) => !activeKeys.has(key));
  return { ...manifest, orphanedKeys };
};
