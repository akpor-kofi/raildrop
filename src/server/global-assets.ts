import { createHash } from 'node:crypto';

import { encodeNamespace, namespace } from '../core';

import type { RaildropStorage, RaildropStoredObject } from './storage';

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

export interface GlobalAssetMirrorTarget {
  name: string;
  storage: RaildropStorage;
}

export interface GlobalAssetMirrorResult {
  targets: Record<string, { copied: number; verified: number }>;
  objectCount: number;
  manifestAssetCount: number;
}

export const GLOBAL_MANIFEST_KEY = 'public/global/_raildrop/manifest.json';

const GLOBAL_ASSET_ALIAS_PATTERN = /^[a-z0-9][a-z0-9-]{0,119}$/;
const SHA256_PATTERN = /^[a-f0-9]{64}$/;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

const isGlobalManifestEntry = (value: unknown): value is GlobalAssetManifestEntry => {
  if (!isRecord(value)) return false;
  if (
    typeof value.key !== 'string' ||
    !value.key.startsWith('public/global/') ||
    value.key === GLOBAL_MANIFEST_KEY ||
    typeof value.checksum !== 'string' ||
    !SHA256_PATTERN.test(value.checksum) ||
    !value.key.includes(`/${value.checksum}/file.`) ||
    typeof value.size !== 'number' ||
    !Number.isSafeInteger(value.size) ||
    value.size < 0 ||
    typeof value.contentType !== 'string' ||
    value.contentType.length === 0 ||
    typeof value.filename !== 'string' ||
    value.filename.length === 0
  ) {
    return false;
  }
  return true;
};

const parseGlobalManifest = async (
  storage: RaildropStorage
): Promise<GlobalAssetManifest | null> => {
  const object = await storage.get(GLOBAL_MANIFEST_KEY);
  if (!object) return null;
  const parsed: unknown = JSON.parse(await new Response(object.body as BodyInit).text());
  if (
    !isRecord(parsed) ||
    parsed.version !== 1 ||
    typeof parsed.generatedAt !== 'string' ||
    !isRecord(parsed.assets) ||
    !Object.entries(parsed.assets).every(
      ([alias, entry]) => GLOBAL_ASSET_ALIAS_PATTERN.test(alias) && isGlobalManifestEntry(entry)
    )
  ) {
    throw new Error('Invalid global asset manifest encountered.');
  }
  return parsed as unknown as GlobalAssetManifest;
};

const writeGlobalAssetEntries = async (
  storage: RaildropStorage,
  sources: readonly GlobalAssetSource[]
): Promise<Record<string, GlobalAssetManifestEntry>> => {
  const assets: Record<string, GlobalAssetManifestEntry> = {};
  const aliases = new Set<string>();
  for (const source of sources) {
    if (!GLOBAL_ASSET_ALIAS_PATTERN.test(source.alias)) {
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
  return assets;
};

const writeGlobalManifest = async (
  storage: RaildropStorage,
  assets: Record<string, GlobalAssetManifestEntry>
): Promise<GlobalAssetManifest> => {
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
  return manifest;
};

const findOrphanedKeys = (
  previousAssets: Record<string, unknown>,
  activeAssets: Record<string, GlobalAssetManifestEntry>
): string[] => {
  const activeKeys = new Set(Object.values(activeAssets).map((asset) => asset.key));
  const previousKeys = Object.values(previousAssets).flatMap((asset) =>
    isRecord(asset) && typeof asset.key === 'string' ? [asset.key] : []
  );
  return [...new Set(previousKeys)].filter((key) => !activeKeys.has(key));
};

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
  const assets = await writeGlobalAssetEntries(storage, sources);
  const manifest = await writeGlobalManifest(storage, assets);
  const previousAssets: Record<string, unknown> =
    isRecord(previousManifest) && isRecord(previousManifest.assets) ? previousManifest.assets : {};
  const orphanedKeys = findOrphanedKeys(previousAssets, assets);
  return { ...manifest, orphanedKeys };
};

export const upsertGlobalAssets = async (
  storage: RaildropStorage,
  sources: readonly GlobalAssetSource[]
): Promise<GlobalAssetSyncResult> => {
  const previousManifest = await parseGlobalManifest(storage);
  const upsertedAssets = await writeGlobalAssetEntries(storage, sources);
  const assets = { ...(previousManifest?.assets ?? {}), ...upsertedAssets };
  const manifest = await writeGlobalManifest(storage, assets);
  const orphanedKeys = findOrphanedKeys(previousManifest?.assets ?? {}, assets);
  return { ...manifest, orphanedKeys };
};

const readObjectBytes = async (storage: RaildropStorage, key: string): Promise<Uint8Array> => {
  const object = await storage.get(key);
  if (!object) throw new Error(`Global asset disappeared while mirroring: ${key}`);
  return new Uint8Array(await new Response(object.body as BodyInit).arrayBuffer());
};

const listAllGlobalObjects = async (storage: RaildropStorage) => {
  const objects = [];
  let continuationToken: string | undefined;
  do {
    const page = await storage.list('public/global/', continuationToken);
    objects.push(...page.objects);
    continuationToken = page.nextToken;
  } while (continuationToken);
  return objects;
};

export const mirrorGlobalAssets = async (
  targets: readonly GlobalAssetMirrorTarget[]
): Promise<GlobalAssetMirrorResult> => {
  if (targets.length < 2) throw new Error('At least two Raildrop mirror targets are required.');

  const inventories = await Promise.all(
    targets.map(async (target) => ({
      ...target,
      objects: await listAllGlobalObjects(target.storage),
      manifest: await parseGlobalManifest(target.storage),
    }))
  );
  const objectSources = new Map<
    string,
    { storage: RaildropStorage; object: RaildropStoredObject }
  >();
  for (const inventory of inventories) {
    for (const object of inventory.objects) {
      if (object.key === GLOBAL_MANIFEST_KEY) continue;
      const existing = objectSources.get(object.key);
      if (existing && existing.object.size !== object.size) {
        throw new Error(`Global asset conflict for ${object.key}; refusing to overwrite it.`);
      }
      if (!existing) objectSources.set(object.key, { storage: inventory.storage, object });
    }
  }

  const canonicalObjects = new Map<
    string,
    {
      body: Uint8Array;
      checksum: string;
      contentType: string;
      cacheControl?: string;
      metadata: Record<string, string>;
    }
  >();

  await Promise.all(
    [...objectSources.entries()].map(async ([key, source]) => {
      const body = await readObjectBytes(source.storage, key);
      const checksum = createHash('sha256').update(body).digest('hex');
      canonicalObjects.set(key, {
        body,
        checksum,
        contentType: source.object.contentType ?? 'application/octet-stream',
        ...(source.object.cacheControl ? { cacheControl: source.object.cacheControl } : {}),
        metadata: source.object.metadata,
      });
    })
  );

  const assets: Record<string, GlobalAssetManifestEntry> = {};
  for (const inventory of inventories) {
    for (const [alias, entry] of Object.entries(inventory.manifest?.assets ?? {})) {
      const existing = assets[alias];
      if (existing && (existing.key !== entry.key || existing.checksum !== entry.checksum)) {
        throw new Error(`Global asset alias conflict for ${alias}; refusing to merge manifests.`);
      }
      assets[alias] = entry;
    }
  }
  for (const [alias, entry] of Object.entries(assets)) {
    const object = canonicalObjects.get(entry.key);
    if (!object) throw new Error(`Manifest entry ${alias} has no available global object.`);
    if (object.checksum !== entry.checksum || object.body.byteLength !== entry.size) {
      throw new Error(`Manifest entry ${alias} does not match an available global object.`);
    }
  }

  const result: GlobalAssetMirrorResult = {
    targets: {},
    objectCount: canonicalObjects.size + 1,
    manifestAssetCount: Object.keys(assets).length,
  };
  await Promise.all(
    targets.map(async (target) => {
      const outcomes = await Promise.all(
        [...canonicalObjects.entries()].map(async ([key, object]) => {
          const current = await target.storage.get(key);
          const currentBytes = current
            ? new Uint8Array(await new Response(current.body as BodyInit).arrayBuffer())
            : null;
          const currentChecksum = currentBytes
            ? createHash('sha256').update(currentBytes).digest('hex')
            : null;
          if (currentChecksum && currentChecksum !== object.checksum) {
            throw new Error(
              `Global asset conflict for ${key} in ${target.name}; refusing to overwrite it.`
            );
          }
          if (!current) {
            await target.storage.put({
              key,
              body: object.body,
              contentType: object.contentType,
              cacheControl: object.cacheControl,
              metadata: object.metadata,
            });
          }
          const stored = await readObjectBytes(target.storage, key);
          if (createHash('sha256').update(stored).digest('hex') !== object.checksum) {
            throw new Error(`Global asset verification failed for ${key} in ${target.name}.`);
          }
          return { copied: current ? 0 : 1 };
        })
      );
      await target.storage.put({
        key: GLOBAL_MANIFEST_KEY,
        body: JSON.stringify({ version: 1, generatedAt: new Date().toISOString(), assets }),
        contentType: 'application/json',
        cacheControl: 'public, max-age=300, stale-while-revalidate=86400',
        metadata: {
          'raildrop-access': 'public',
          'raildrop-retention': 'permanent',
        },
      });
      result.targets[target.name] = {
        copied: outcomes.reduce((total, outcome) => total + outcome.copied, 0),
        verified: outcomes.length + 1,
      };
    })
  );
  return result;
};
