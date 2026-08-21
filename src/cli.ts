#!/usr/bin/env node
import { readFile, readdir } from 'node:fs/promises';
import { extname, parse, resolve } from 'node:path';

import { cleanupExpiredObjects } from './server/cleanup';
import { mirrorGlobalAssets, syncGlobalAssets, upsertGlobalAssets } from './server/global-assets';
import { RailwayBucketStorage, railwayBucketConfigFromEnv } from './server/storage';

import type { GlobalAssetSyncResult } from './server/global-assets';
import type { RaildropBucketConfig } from './server/storage';

interface ManifestFile {
  assets: {
    alias: string;
    source: string;
    filename?: string;
    contentType: string;
    namespace: string[];
  }[];
  directories?: {
    source: string;
    aliasPrefix: string;
    contentType: string;
    namespace: string[];
    extensions?: string[];
  }[];
}

const mirrorEnvironmentNames = ['development', 'staging', 'production'] as const;

const readMirrorConfig = (environment: (typeof mirrorEnvironmentNames)[number]) => {
  const prefix = `RAILDROP_${environment.toUpperCase()}_`;
  const read = (name: string) => process.env[`${prefix}${name}`]?.trim();
  const bucket = read('BUCKET');
  const endpoint = read('ENDPOINT');
  const accessKeyId = read('ACCESS_KEY_ID');
  const secretAccessKey = read('SECRET_ACCESS_KEY');
  const configured = [bucket, endpoint, accessKeyId, secretAccessKey].filter(Boolean).length;
  if (configured === 0) return null;
  if (configured !== 4) throw new Error(`Incomplete ${environment} Raildrop mirror credentials.`);
  return {
    name: environment,
    config: {
      bucket,
      endpoint,
      accessKeyId,
      secretAccessKey,
      region: read('REGION') ?? 'auto',
      forcePathStyle: read('FORCE_PATH_STYLE') === 'true',
    } as RaildropBucketConfig,
  };
};

const configuredStorages = () => {
  const mirrorTargets = mirrorEnvironmentNames.flatMap((environment) => {
    const target = readMirrorConfig(environment);
    return target ? [{ name: target.name, storage: new RailwayBucketStorage(target.config) }] : [];
  });
  return mirrorTargets.length > 0
    ? mirrorTargets
    : [{ name: 'current', storage: new RailwayBucketStorage(railwayBucketConfigFromEnv()) }];
};

const main = async () => {
  const [command, argument] = process.argv.slice(2);
  const targets = configuredStorages();
  const storage = targets[0]?.storage;
  if (!storage) throw new Error('No Raildrop bucket is configured.');
  if (command === 'health') {
    await storage.list('', undefined);
    process.stdout.write('Raildrop bucket connection is healthy.\n');
    return;
  }
  if (command === 'cleanup') {
    const result = await cleanupExpiredObjects(storage);
    process.stdout.write(`${JSON.stringify(result)}\n`);
    return;
  }
  if (command === 'cors' || command === 'cors:apply') {
    const origins = (process.env.RAILDROP_ALLOWED_ORIGINS ?? '')
      .split(',')
      .map((origin) => origin.trim())
      .filter(Boolean);
    if (origins.length === 0) throw new Error('RAILDROP_ALLOWED_ORIGINS is required.');
    if (command === 'cors:apply') await storage.setCorsRules(origins);
    const rules = await storage.getCorsRules();
    const configured = new Set(rules.flatMap((rule) => rule.AllowedOrigins ?? []));
    const missing = origins.filter((origin) => !configured.has(origin));
    const requiredMethods = ['PUT', 'GET', 'HEAD'];
    const invalid = origins.filter(
      (origin) =>
        !rules.some((rule) => {
          const allowedOrigins = rule.AllowedOrigins ?? [];
          const allowedMethods = new Set(rule.AllowedMethods ?? []);
          const allowedHeaders = rule.AllowedHeaders ?? [];
          return (
            allowedOrigins.includes(origin) &&
            requiredMethods.every((method) => allowedMethods.has(method)) &&
            allowedHeaders.includes('*')
          );
        })
    );
    process.stdout.write(`${JSON.stringify({ origins, missing, invalid, rules }, null, 2)}\n`);
    if (missing.length > 0 || invalid.length > 0) process.exitCode = 1;
    return;
  }
  if ((command === 'sync' || command === 'upsert') && argument) {
    const manifestPath = resolve(argument);
    const manifest = JSON.parse(await readFile(manifestPath, 'utf8')) as ManifestFile;
    const manifestDirectory = resolve(manifestPath, '..');
    const fileSources = await Promise.all(
      manifest.assets.map(async (asset) => {
        const sourcePath = resolve(manifestDirectory, asset.source);
        return {
          alias: asset.alias,
          body: new Uint8Array(await readFile(sourcePath)),
          filename: asset.filename ?? sourcePath.split('/').at(-1) ?? 'asset.bin',
          contentType: asset.contentType,
          namespace: asset.namespace,
        };
      })
    );
    const directorySources = (
      await Promise.all(
        (manifest.directories ?? []).map(async (directory) => {
          const sourceDirectory = resolve(manifestDirectory, directory.source);
          const allowedExtensions = new Set(
            (directory.extensions ?? []).map((extension) => extension.toLowerCase())
          );
          const entries = (await readdir(sourceDirectory, { withFileTypes: true }))
            .filter(
              (entry) =>
                entry.isFile() &&
                (allowedExtensions.size === 0 ||
                  allowedExtensions.has(extname(entry.name).toLowerCase()))
            )
            .sort((left, right) => left.name.localeCompare(right.name));
          return Promise.all(
            entries.map(async (entry) => {
              const sourcePath = resolve(sourceDirectory, entry.name);
              const assetName = parse(entry.name).name;
              const aliasName = assetName.toLowerCase().replace(/[^a-z0-9-]+/g, '-');
              return {
                alias: `${directory.aliasPrefix}${aliasName}`,
                body: new Uint8Array(await readFile(sourcePath)),
                filename: entry.name,
                contentType: directory.contentType,
                namespace: [...directory.namespace, assetName],
              };
            })
          );
        })
      )
    ).flat();
    const sources = [...fileSources, ...directorySources];
    const results: Record<string, GlobalAssetSyncResult> = {};
    await Promise.all(
      targets.map(async (target) => {
        results[target.name] = await (command === 'upsert' ? upsertGlobalAssets : syncGlobalAssets)(
          target.storage,
          sources
        );
      })
    );
    process.stdout.write(`${JSON.stringify(results, null, 2)}\n`);
    return;
  }
  if (command === 'mirror') {
    const result = await mirrorGlobalAssets(targets);
    process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    return;
  }
  process.stderr.write(
    'Usage: raildrop health | cleanup | cors | cors:apply | sync <manifest.json> | upsert <manifest.json> | mirror\n'
  );
  process.exitCode = 1;
};

void main().catch((error: unknown) => {
  process.stderr.write(`${error instanceof Error ? error.message : 'Raildrop command failed.'}\n`);
  process.exitCode = 1;
});
