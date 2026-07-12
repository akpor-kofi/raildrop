#!/usr/bin/env node
import { readFile, readdir } from 'node:fs/promises';
import { extname, parse, resolve } from 'node:path';

import { cleanupExpiredObjects } from './server/cleanup';
import { syncGlobalAssets } from './server/global-assets';
import { RailwayBucketStorage, railwayBucketConfigFromEnv } from './server/storage';

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

const main = async () => {
  const [command, argument] = process.argv.slice(2);
  const storage = new RailwayBucketStorage(railwayBucketConfigFromEnv());
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
  if (command === 'sync' && argument) {
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
    const result = await syncGlobalAssets(storage, sources);
    process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    return;
  }
  process.stderr.write(
    'Usage: raildrop health | cleanup | cors | cors:apply | sync <manifest.json>\n'
  );
  process.exitCode = 1;
};

void main().catch((error: unknown) => {
  process.stderr.write(`${error instanceof Error ? error.message : 'Raildrop command failed.'}\n`);
  process.exitCode = 1;
});
