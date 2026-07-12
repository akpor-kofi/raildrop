import {
  AbortMultipartUploadCommand,
  CompleteMultipartUploadCommand,
  CopyObjectCommand,
  CreateMultipartUploadCommand,
  DeleteObjectsCommand,
  GetObjectCommand,
  GetBucketCorsCommand,
  HeadObjectCommand,
  ListMultipartUploadsCommand,
  ListObjectsV2Command,
  PutObjectCommand,
  PutBucketCorsCommand,
  S3Client,
  UploadPartCommand,
} from '@aws-sdk/client-s3';
import { getSignedUrl } from '@aws-sdk/s3-request-presigner';

import type { CompletedPart } from '@aws-sdk/client-s3';

export interface RaildropBucketConfig {
  bucket: string;
  endpoint: string;
  region?: string;
  accessKeyId: string;
  secretAccessKey: string;
  forcePathStyle?: boolean;
}

export interface RaildropStoredObject {
  key: string;
  size: number;
  contentType: string | null;
  etag: string | null;
  lastModified: Date | null;
  metadata: Record<string, string>;
  contentDisposition?: string | null;
  cacheControl?: string | null;
}

export interface RaildropObjectBody extends RaildropStoredObject {
  body: ReadableStream<Uint8Array> | Blob | ArrayBuffer | Uint8Array;
  contentRange: string | null;
}

export interface RaildropListResult {
  objects: RaildropStoredObject[];
  nextToken?: string;
}

export interface RaildropMultipartUpload {
  key: string;
  uploadId: string;
  initiated: Date | null;
}

export interface RaildropStorage {
  presignPut(args: {
    key: string;
    contentType: string;
    metadata: Record<string, string>;
    expiresIn: number;
  }): Promise<string>;
  createMultipart(args: {
    key: string;
    contentType: string;
    metadata: Record<string, string>;
  }): Promise<string>;
  presignPart(args: {
    key: string;
    uploadId: string;
    partNumber: number;
    expiresIn: number;
  }): Promise<string>;
  completeMultipart(args: { key: string; uploadId: string; parts: CompletedPart[] }): Promise<void>;
  abortMultipart(args: { key: string; uploadId: string }): Promise<void>;
  head(key: string): Promise<RaildropStoredObject | null>;
  get(key: string, range?: string): Promise<RaildropObjectBody | null>;
  put(args: {
    key: string;
    body: Uint8Array | string;
    contentType: string;
    cacheControl?: string;
    metadata?: Record<string, string>;
  }): Promise<RaildropStoredObject>;
  delete(keys: readonly string[]): Promise<void>;
  copy(sourceKey: string, destinationKey: string): Promise<void>;
  list(prefix: string, continuationToken?: string): Promise<RaildropListResult>;
  listMultipart(prefix: string): Promise<RaildropMultipartUpload[]>;
  getSignedUrl(key: string, expiresIn: number, downloadName?: string): Promise<string>;
}

const trimEtag = (etag: string | undefined): string | null => etag?.replace(/^"|"$/g, '') ?? null;

export class RailwayBucketStorage implements RaildropStorage {
  readonly client: S3Client;

  constructor(
    readonly config: RaildropBucketConfig,
    client?: S3Client
  ) {
    this.client =
      client ??
      new S3Client({
        endpoint: config.endpoint,
        region: config.region ?? 'auto',
        forcePathStyle: config.forcePathStyle ?? false,
        credentials: {
          accessKeyId: config.accessKeyId,
          secretAccessKey: config.secretAccessKey,
        },
      });
  }

  async presignPut(args: {
    key: string;
    contentType: string;
    metadata: Record<string, string>;
    expiresIn: number;
  }): Promise<string> {
    return getSignedUrl(
      this.client,
      new PutObjectCommand({
        Bucket: this.config.bucket,
        Key: args.key,
        ContentType: args.contentType,
        Metadata: args.metadata,
      }),
      { expiresIn: args.expiresIn }
    );
  }

  async createMultipart(args: {
    key: string;
    contentType: string;
    metadata: Record<string, string>;
  }): Promise<string> {
    const result = await this.client.send(
      new CreateMultipartUploadCommand({
        Bucket: this.config.bucket,
        Key: args.key,
        ContentType: args.contentType,
        Metadata: args.metadata,
      })
    );
    if (!result.UploadId) throw new Error('Railway did not return a multipart upload id.');
    return result.UploadId;
  }

  async presignPart(args: {
    key: string;
    uploadId: string;
    partNumber: number;
    expiresIn: number;
  }): Promise<string> {
    return getSignedUrl(
      this.client,
      new UploadPartCommand({
        Bucket: this.config.bucket,
        Key: args.key,
        UploadId: args.uploadId,
        PartNumber: args.partNumber,
      }),
      { expiresIn: args.expiresIn }
    );
  }

  async completeMultipart(args: {
    key: string;
    uploadId: string;
    parts: CompletedPart[];
  }): Promise<void> {
    await this.client.send(
      new CompleteMultipartUploadCommand({
        Bucket: this.config.bucket,
        Key: args.key,
        UploadId: args.uploadId,
        MultipartUpload: { Parts: args.parts },
      })
    );
  }

  async abortMultipart(args: { key: string; uploadId: string }): Promise<void> {
    await this.client.send(
      new AbortMultipartUploadCommand({
        Bucket: this.config.bucket,
        Key: args.key,
        UploadId: args.uploadId,
      })
    );
  }

  async head(key: string): Promise<RaildropStoredObject | null> {
    try {
      const result = await this.client.send(
        new HeadObjectCommand({ Bucket: this.config.bucket, Key: key })
      );
      return {
        key,
        size: result.ContentLength ?? 0,
        contentType: result.ContentType ?? null,
        etag: trimEtag(result.ETag),
        lastModified: result.LastModified ?? null,
        metadata: result.Metadata ?? {},
        contentDisposition: result.ContentDisposition ?? null,
        cacheControl: result.CacheControl ?? null,
      };
    } catch (error) {
      const status = (error as { $metadata?: { httpStatusCode?: number } }).$metadata
        ?.httpStatusCode;
      if (status === 404) return null;
      throw error;
    }
  }

  async get(key: string, range?: string): Promise<RaildropObjectBody | null> {
    try {
      const result = await this.client.send(
        new GetObjectCommand({ Bucket: this.config.bucket, Key: key, Range: range })
      );
      if (!result.Body) return null;
      const body = result.Body.transformToWebStream();
      return {
        key,
        body,
        size: result.ContentLength ?? 0,
        contentType: result.ContentType ?? null,
        contentDisposition: result.ContentDisposition ?? null,
        contentRange: result.ContentRange ?? null,
        cacheControl: result.CacheControl ?? null,
        etag: trimEtag(result.ETag),
        lastModified: result.LastModified ?? null,
        metadata: result.Metadata ?? {},
      };
    } catch (error) {
      const status = (error as { $metadata?: { httpStatusCode?: number } }).$metadata
        ?.httpStatusCode;
      if (status === 404) return null;
      throw error;
    }
  }

  async put(args: {
    key: string;
    body: Uint8Array | string;
    contentType: string;
    cacheControl?: string;
    metadata?: Record<string, string>;
  }): Promise<RaildropStoredObject> {
    await this.client.send(
      new PutObjectCommand({
        Bucket: this.config.bucket,
        Key: args.key,
        Body: args.body,
        ContentType: args.contentType,
        CacheControl: args.cacheControl,
        Metadata: args.metadata,
      })
    );
    const stored = await this.head(args.key);
    if (!stored) throw new Error('Uploaded object could not be verified.');
    return stored;
  }

  async delete(keys: readonly string[]): Promise<void> {
    for (let index = 0; index < keys.length; index += 1000) {
      const chunk = keys.slice(index, index + 1000);
      if (chunk.length === 0) continue;
      const result = await this.client.send(
        new DeleteObjectsCommand({
          Bucket: this.config.bucket,
          Delete: { Objects: chunk.map((Key) => ({ Key })), Quiet: true },
        })
      );
      if (result.Errors && result.Errors.length > 0) {
        throw new Error(`Railway failed to delete ${result.Errors.length} object(s).`);
      }
    }
  }

  async copy(sourceKey: string, destinationKey: string): Promise<void> {
    await this.client.send(
      new CopyObjectCommand({
        Bucket: this.config.bucket,
        Key: destinationKey,
        CopySource: `${this.config.bucket}/${encodeURIComponent(sourceKey).replace(/%2F/g, '/')}`,
      })
    );
  }

  async list(prefix: string, continuationToken?: string): Promise<RaildropListResult> {
    const result = await this.client.send(
      new ListObjectsV2Command({
        Bucket: this.config.bucket,
        Prefix: prefix,
        ContinuationToken: continuationToken,
      })
    );
    return {
      objects: (result.Contents ?? []).flatMap((entry) =>
        entry.Key
          ? [
              {
                key: entry.Key,
                size: entry.Size ?? 0,
                contentType: null,
                etag: trimEtag(entry.ETag),
                lastModified: entry.LastModified ?? null,
                metadata: {},
              },
            ]
          : []
      ),
      nextToken: result.NextContinuationToken,
    };
  }

  async listMultipart(prefix: string): Promise<RaildropMultipartUpload[]> {
    const uploads: RaildropMultipartUpload[] = [];
    let keyMarker: string | undefined;
    let uploadIdMarker: string | undefined;
    let hasMore = true;
    while (hasMore) {
      const result = await this.client.send(
        new ListMultipartUploadsCommand({
          Bucket: this.config.bucket,
          Prefix: prefix,
          KeyMarker: keyMarker,
          UploadIdMarker: uploadIdMarker,
        })
      );
      uploads.push(
        ...(result.Uploads ?? []).flatMap((upload) =>
          upload.Key && upload.UploadId
            ? [{ key: upload.Key, uploadId: upload.UploadId, initiated: upload.Initiated ?? null }]
            : []
        )
      );
      hasMore = result.IsTruncated === true;
      keyMarker = hasMore ? result.NextKeyMarker : undefined;
      uploadIdMarker = hasMore ? result.NextUploadIdMarker : undefined;
      if (hasMore && !keyMarker && !uploadIdMarker) {
        throw new Error('Railway returned a truncated multipart listing without continuation.');
      }
    }
    return uploads;
  }

  async getSignedUrl(key: string, expiresIn: number, downloadName?: string): Promise<string> {
    return getSignedUrl(
      this.client,
      new GetObjectCommand({
        Bucket: this.config.bucket,
        Key: key,
        ResponseContentDisposition: downloadName
          ? `attachment; filename="${downloadName.replace(/["\\]/g, '_')}"`
          : undefined,
      }),
      { expiresIn }
    );
  }

  async getCorsRules() {
    const result = await this.client.send(new GetBucketCorsCommand({ Bucket: this.config.bucket }));
    return result.CORSRules ?? [];
  }

  async setCorsRules(origins: readonly string[]): Promise<void> {
    await this.client.send(
      new PutBucketCorsCommand({
        Bucket: this.config.bucket,
        CORSConfiguration: {
          CORSRules: [
            {
              AllowedOrigins: [...origins],
              AllowedMethods: ['PUT', 'GET', 'HEAD'],
              // Railway's S3 CORS implementation does not currently match
              // partial header wildcards such as `x-amz-*`. Presigned uploads
              // may also gain SDK-managed headers over time, so constrain the
              // origin and methods while allowing every request header.
              AllowedHeaders: ['*'],
              ExposeHeaders: ['etag'],
              MaxAgeSeconds: 600,
            },
          ],
        },
      })
    );
  }
}

export const railwayBucketConfigFromEnv = (
  env: Record<string, string | undefined> = process.env
): RaildropBucketConfig => {
  const required = (raildropName: string, railwayName: string) => {
    const value = env[raildropName] ?? env[railwayName];
    if (!value) throw new Error(`Missing ${raildropName} (or Railway ${railwayName}).`);
    return value;
  };
  return {
    bucket: required('RAILDROP_BUCKET', 'BUCKET'),
    endpoint: required('RAILDROP_ENDPOINT', 'ENDPOINT'),
    region: env.RAILDROP_REGION ?? env.REGION ?? 'auto',
    accessKeyId: required('RAILDROP_ACCESS_KEY_ID', 'ACCESS_KEY_ID'),
    secretAccessKey: required('RAILDROP_SECRET_ACCESS_KEY', 'SECRET_ACCESS_KEY'),
    forcePathStyle: env.RAILDROP_FORCE_PATH_STYLE === 'true',
  };
};
