import { createRouteHandler } from '../server/handler';

/* eslint-disable @typescript-eslint/require-await */
import type { FileRouter } from '../server/router';
import type {
  RaildropListResult,
  RaildropMultipartUpload,
  RaildropObjectBody,
  RaildropStorage,
  RaildropStoredObject,
} from '../server/storage';

interface FakeEntry {
  body: Uint8Array;
  contentType: string;
  cacheControl: string | null;
  metadata: Record<string, string>;
  etag: string;
  lastModified: Date;
}

export class FakeRaildropStorage implements RaildropStorage {
  readonly objects = new Map<string, FakeEntry>();
  readonly multipart = new Map<string, RaildropMultipartUpload>();

  async presignPut(args: { key: string }): Promise<string> {
    return `https://storage.test/${encodeURIComponent(args.key)}`;
  }

  async createMultipart(args: { key: string }): Promise<string> {
    const uploadId = `multipart-${this.multipart.size + 1}`;
    this.multipart.set(uploadId, { key: args.key, uploadId, initiated: new Date() });
    return uploadId;
  }

  async presignPart(args: { key: string; uploadId: string; partNumber: number }): Promise<string> {
    return `https://storage.test/${encodeURIComponent(args.key)}?uploadId=${args.uploadId}&part=${args.partNumber}`;
  }

  async completeMultipart(args: { key: string; uploadId: string }): Promise<void> {
    this.multipart.delete(args.uploadId);
    if (!this.objects.has(args.key)) {
      this.objects.set(args.key, {
        body: new Uint8Array(),
        contentType: 'application/octet-stream',
        cacheControl: null,
        metadata: {},
        etag: 'fake-multipart',
        lastModified: new Date(),
      });
    }
  }

  async abortMultipart(args: { uploadId: string }): Promise<void> {
    this.multipart.delete(args.uploadId);
  }

  async head(key: string): Promise<RaildropStoredObject | null> {
    const entry = this.objects.get(key);
    return entry ? this.toStored(key, entry) : null;
  }

  async get(key: string): Promise<RaildropObjectBody | null> {
    const entry = this.objects.get(key);
    if (!entry) return null;
    return {
      ...this.toStored(key, entry),
      body: entry.body,
      contentDisposition: null,
      contentRange: null,
      cacheControl: entry.cacheControl,
    };
  }

  async put(args: {
    key: string;
    body: Uint8Array | string;
    contentType: string;
    cacheControl?: string;
    metadata?: Record<string, string>;
  }): Promise<RaildropStoredObject> {
    const body = typeof args.body === 'string' ? new TextEncoder().encode(args.body) : args.body;
    const entry: FakeEntry = {
      body,
      contentType: args.contentType,
      cacheControl: args.cacheControl ?? null,
      metadata: args.metadata ?? {},
      etag: `etag-${body.byteLength}`,
      lastModified: new Date(),
    };
    this.objects.set(args.key, entry);
    return this.toStored(args.key, entry);
  }

  async delete(keys: readonly string[]): Promise<void> {
    keys.forEach((key) => this.objects.delete(key));
  }

  async copy(sourceKey: string, destinationKey: string): Promise<void> {
    const source = this.objects.get(sourceKey);
    if (!source) throw new Error('Source does not exist.');
    this.objects.set(destinationKey, { ...source, body: source.body.slice() });
  }

  async list(prefix: string): Promise<RaildropListResult> {
    return {
      objects: [...this.objects.entries()]
        .filter(([key]) => key.startsWith(prefix))
        .map(([key, entry]) => this.toStored(key, entry)),
    };
  }

  async listMultipart(prefix: string): Promise<RaildropMultipartUpload[]> {
    return [...this.multipart.values()].filter((upload) => upload.key.startsWith(prefix));
  }

  async getSignedUrl(key: string, expiresIn: number): Promise<string> {
    return `https://storage.test/${encodeURIComponent(key)}?expiresIn=${expiresIn}`;
  }

  private toStored(key: string, entry: FakeEntry): RaildropStoredObject {
    return {
      key,
      size: entry.body.byteLength,
      contentType: entry.contentType,
      etag: entry.etag,
      lastModified: entry.lastModified,
      metadata: entry.metadata,
    };
  }
}

export const createRaildropTestHarness = <TRouter extends FileRouter>(router: TRouter) => {
  const storage = new FakeRaildropStorage();
  const handler = createRouteHandler({
    router,
    storage,
    secret: 'raildrop-testing-secret-at-least-thirty-two-characters',
    publicBaseUrl: 'https://assets.raildrop.test',
  });
  return { storage, handler };
};
