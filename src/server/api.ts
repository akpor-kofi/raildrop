import { createHash, randomUUID } from 'node:crypto';

import { encodeNamespace, RaildropError, sanitizeDownloadName } from '../core';

import type { RaildropAccess, RaildropRetention } from '../core';
import type { RaildropStorage } from './storage';

export interface RaildropServerUpload {
  body: Uint8Array;
  name: string;
  type: string;
  namespace: readonly string[];
  access?: RaildropAccess;
  retention?: RaildropRetention;
  expiresAt?: Date;
}

export class RaildropApi {
  constructor(
    readonly storage: RaildropStorage,
    readonly publicBaseUrl: string
  ) {}

  async uploadFiles(file: RaildropServerUpload) {
    const id = randomUUID();
    const access = file.access ?? 'public';
    const retention = file.retention ?? 'permanent';
    if (retention === 'permanent' && file.expiresAt) {
      throw new RaildropError('BAD_REQUEST', 'Permanent uploads cannot have an expiry.');
    }
    const expiry =
      file.expiresAt ?? (retention === 'temporary' ? new Date(Date.now() + 604_800_000) : null);
    if (expiry && (!Number.isFinite(expiry.getTime()) || expiry.getTime() <= Date.now())) {
      throw new RaildropError('BAD_REQUEST', 'Temporary upload expiry must be in the future.');
    }
    const hash = createHash('sha256').update(file.body).digest('hex');
    const extension = /\.([a-z0-9]{1,12})$/i.exec(file.name)?.[1]?.toLowerCase() ?? 'bin';
    const temporaryPrefix =
      retention === 'temporary' ? `/tmp/${Math.floor((expiry?.getTime() ?? 0) / 86_400_000)}` : '';
    const key = `${access}${temporaryPrefix}/${encodeNamespace(file.namespace)}/${id}/file.${extension}`;
    const stored = await this.storage.put({
      key,
      body: file.body,
      contentType: file.type,
      cacheControl:
        access === 'public' ? 'public, max-age=31536000, immutable' : 'private, no-store',
      metadata: {
        'raildrop-id': id,
        'raildrop-access': access,
        'raildrop-retention': retention,
        'raildrop-checksum': hash,
        'raildrop-name': Buffer.from(sanitizeDownloadName(file.name)).toString('base64url'),
        ...(expiry ? { 'raildrop-expires-at': expiry.toISOString() } : {}),
      },
    });
    return {
      id,
      key,
      access,
      retention,
      publicUrl: access === 'public' ? `${this.publicBaseUrl.replace(/\/$/, '')}/${key}` : null,
      name: file.name,
      type: file.type,
      size: file.body.byteLength,
      etag: stored.etag,
      checksum: hash,
      expiresAt: expiry?.toISOString() ?? null,
    };
  }

  deleteFiles(keys: readonly string[]) {
    return this.storage.delete(keys);
  }

  getSignedUrl(key: string, options: { expiresIn?: number; downloadName?: string } = {}) {
    if (!key.startsWith('private/') && !key.startsWith('public/')) {
      throw new RaildropError('BAD_REQUEST', 'Invalid Raildrop object key.');
    }
    const expiresIn = options.expiresIn ?? 300;
    if (!Number.isSafeInteger(expiresIn) || expiresIn <= 0 || expiresIn > 604_800) {
      throw new RaildropError(
        'BAD_REQUEST',
        'Signed URL expiry must be between 1 and 604800 seconds.'
      );
    }
    return this.storage.getSignedUrl(
      key,
      expiresIn,
      options.downloadName ? sanitizeDownloadName(options.downloadName) : undefined
    );
  }

  headFile(key: string) {
    return this.storage.head(key);
  }

  copyFiles(sourceKey: string, destinationKey: string) {
    return this.storage.copy(sourceKey, destinationKey);
  }

  listFiles(prefix: string, continuationToken?: string) {
    return this.storage.list(prefix, continuationToken);
  }
}
