import {
  RaildropError,
  encodeNamespace,
  mimeGroupFor,
  parseFileSize,
  sanitizeDownloadName,
} from '../core';
import { createSessionToken, createUploadId, readSessionToken } from './session';

import type {
  RaildropFinalizeRequest,
  RaildropAbortRequest,
  RaildropPrepareRequest,
  RaildropPreparedFile,
  RaildropRequestedFile,
  RaildropUploadedFile,
} from '../core';
import type { FileRouter, RaildropPolicyOverride, RaildropRoute } from './router';
import type { RaildropStorage } from './storage';

const DEFAULT_UPLOAD_EXPIRY_SECONDS = 60 * 60;
const DEFAULT_MULTIPART_THRESHOLD = 16 * 1_000_000;
const DEFAULT_PART_SIZE = 8 * 1_000_000;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

const isRequestedFile = (value: unknown): value is RaildropRequestedFile =>
  isRecord(value) &&
  typeof value.name === 'string' &&
  typeof value.size === 'number' &&
  typeof value.type === 'string' &&
  (value.lastModified === undefined || typeof value.lastModified === 'number');

const readPrepareRequest = (value: unknown): RaildropPrepareRequest => {
  if (
    !isRecord(value) ||
    value.action !== 'prepare' ||
    typeof value.endpoint !== 'string' ||
    !Array.isArray(value.files) ||
    !value.files.every(isRequestedFile)
  ) {
    throw new RaildropError('BAD_REQUEST', 'Invalid prepare request.');
  }
  return value as unknown as RaildropPrepareRequest;
};

const readSessionRequest = (
  value: unknown,
  action: 'finalize' | 'abort'
): RaildropFinalizeRequest | RaildropAbortRequest => {
  if (
    !isRecord(value) ||
    value.action !== action ||
    typeof value.endpoint !== 'string' ||
    typeof value.sessionToken !== 'string'
  ) {
    throw new RaildropError('BAD_REQUEST', `Invalid ${action} request.`);
  }
  return value as unknown as RaildropFinalizeRequest | RaildropAbortRequest;
};

const policyFromMetadata = (metadata: unknown): RaildropPolicyOverride => {
  if (!metadata || typeof metadata !== 'object' || !('$raildrop' in metadata)) return {};
  const value = (metadata as { $raildrop?: unknown }).$raildrop;
  if (!value || typeof value !== 'object') return {};
  const policy = value as Record<string, unknown>;
  return {
    access: policy.access === 'public' || policy.access === 'private' ? policy.access : undefined,
    retention:
      policy.retention === 'permanent' || policy.retention === 'temporary'
        ? policy.retention
        : undefined,
    temporaryTtlSeconds:
      typeof policy.temporaryTtlSeconds === 'number' ? policy.temporaryTtlSeconds : undefined,
  };
};

export interface RaildropHandlerConfig<TRouter extends FileRouter> {
  router: TRouter;
  storage: RaildropStorage;
  secret: string;
  publicBaseUrl: string;
  uploadExpirySeconds?: number;
  multipartThresholdBytes?: number;
  multipartPartSizeBytes?: number;
}

const json = (body: unknown, status = 200): Response =>
  Response.json(body, {
    status,
    headers: {
      'Cache-Control': 'no-store',
      'X-Content-Type-Options': 'nosniff',
    },
  });

const extensionFor = (file: RaildropRequestedFile): string => {
  const extension = /\.([a-z0-9]{1,12})$/i.exec(file.name)?.[1]?.toLowerCase();
  if (extension) return extension;
  const subtype = file.type
    .split('/')[1]
    ?.split('+')[0]
    ?.replace(/[^a-z0-9]/gi, '');
  return subtype?.toLowerCase() ?? 'bin';
};

const resolveRule = (route: RaildropRoute, file: RaildropRequestedFile) => {
  const exact = route._def.rules[file.type];
  const group = route._def.rules[mimeGroupFor(file.type)];
  return exact ?? group;
};

const validateFiles = (route: RaildropRoute, files: RaildropRequestedFile[]): void => {
  if (files.length === 0) throw new RaildropError('BAD_REQUEST', 'At least one file is required.');
  const counts = new Map<string, number>();
  for (const file of files) {
    if (!file.name || !Number.isSafeInteger(file.size) || file.size <= 0 || !file.type) {
      throw new RaildropError('BAD_REQUEST', 'File metadata is incomplete.');
    }
    const exactKey = route._def.rules[file.type] ? file.type : mimeGroupFor(file.type);
    const rule = resolveRule(route, file);
    if (!rule) throw new RaildropError('INVALID_TYPE', `File type ${file.type} is not allowed.`);
    if (rule.mimeTypes && !rule.mimeTypes.includes(file.type)) {
      throw new RaildropError('INVALID_TYPE', `File type ${file.type} is not allowed.`);
    }
    if (file.size > parseFileSize(rule.maxFileSize)) {
      throw new RaildropError('TOO_LARGE', `${file.name} exceeds the route size limit.`);
    }
    const count = (counts.get(exactKey) ?? 0) + 1;
    counts.set(exactKey, count);
    if (count > (rule.maxFileCount ?? 1)) {
      throw new RaildropError('BAD_REQUEST', `Too many ${exactKey} files.`);
    }
  }
};

const createObjectKey = (args: {
  access: 'public' | 'private';
  retention: 'permanent' | 'temporary';
  expiresAt: Date | null;
  namespace: readonly string[];
  uploadId: string;
  file: RaildropRequestedFile;
}): string => {
  const prefix =
    args.retention === 'temporary'
      ? `${args.access}/tmp/${Math.floor((args.expiresAt?.getTime() ?? 0) / 86_400_000)}`
      : args.access;
  return `${prefix}/${encodeNamespace(args.namespace)}/${args.uploadId}/file.${extensionFor(args.file)}`;
};

const errorResponse = (error: unknown): Response => {
  if (error instanceof RaildropError) {
    const status =
      error.code === 'FORBIDDEN'
        ? 403
        : error.code === 'NOT_FOUND'
          ? 404
          : error.code === 'TOO_LARGE'
            ? 413
            : error.code === 'STORAGE_ERROR' || error.code === 'CALLBACK_ERROR'
              ? 502
              : 400;
    return json({ error: { code: error.code, message: error.message } }, status);
  }
  return json({ error: { code: 'STORAGE_ERROR', message: 'Raildrop request failed.' } }, 500);
};

const getRoute = <TRouter extends FileRouter>(router: TRouter, endpoint: string): RaildropRoute => {
  const route = router[endpoint];
  if (!route) throw new RaildropError('NOT_FOUND', `Unknown upload endpoint: ${endpoint}`);
  return route;
};

const prepareUploads = async <TRouter extends FileRouter>(
  req: Request,
  body: RaildropPrepareRequest,
  config: RaildropHandlerConfig<TRouter>
): Promise<Response> => {
  const route = getRoute(config.router, body.endpoint);
  validateFiles(route, body.files);
  const metadata = await route._def.middleware({ req, input: body.input, files: body.files });
  const override = policyFromMetadata(metadata);
  const access = override.access ?? route._def.access;
  const retention = override.retention ?? route._def.retention;
  const temporaryTtlSeconds = override.temporaryTtlSeconds ?? route._def.temporaryTtlSeconds;
  if (!Number.isSafeInteger(temporaryTtlSeconds) || temporaryTtlSeconds <= 0) {
    throw new RaildropError('BAD_REQUEST', 'Temporary retention must be positive.');
  }
  const uploadExpirySeconds = config.uploadExpirySeconds ?? DEFAULT_UPLOAD_EXPIRY_SECONDS;
  if (
    !Number.isSafeInteger(uploadExpirySeconds) ||
    uploadExpirySeconds <= 0 ||
    uploadExpirySeconds > 604_800
  ) {
    throw new RaildropError(
      'BAD_REQUEST',
      'Upload URL expiry must be between 1 and 604800 seconds.'
    );
  }
  const uploadExpiresAt = Date.now() + uploadExpirySeconds * 1000;
  const multipartThreshold = config.multipartThresholdBytes ?? DEFAULT_MULTIPART_THRESHOLD;
  if (!Number.isSafeInteger(multipartThreshold) || multipartThreshold <= 0) {
    throw new RaildropError('BAD_REQUEST', 'Multipart threshold must be a positive integer.');
  }
  const partSize = Math.max(config.multipartPartSizeBytes ?? DEFAULT_PART_SIZE, 5_000_000);
  if (!Number.isSafeInteger(partSize)) {
    throw new RaildropError('BAD_REQUEST', 'Multipart part size must be a positive integer.');
  }

  const prepared: RaildropPreparedFile[] = [];
  for (const file of body.files) {
    const id = createUploadId();
    const expiresAt =
      retention === 'temporary' ? new Date(Date.now() + temporaryTtlSeconds * 1000) : null;
    const key = createObjectKey({
      access,
      retention,
      expiresAt,
      namespace: route._def.namespace({ input: body.input, metadata, file }),
      uploadId: id,
      file,
    });
    const objectMetadata = {
      'raildrop-id': id,
      'raildrop-access': access,
      'raildrop-retention': retention,
      'raildrop-name': Buffer.from(sanitizeDownloadName(file.name)).toString('base64url'),
      ...(expiresAt ? { 'raildrop-expires-at': expiresAt.toISOString() } : {}),
    };
    let upload: RaildropPreparedFile['upload'];
    let multipart: { uploadId: string } | null = null;
    if (file.size >= multipartThreshold) {
      const uploadId = await config.storage.createMultipart({
        key,
        contentType: file.type,
        metadata: objectMetadata,
      });
      multipart = { uploadId };
      try {
        const partCount = Math.ceil(file.size / partSize);
        if (partCount > 10_000) {
          throw new RaildropError('TOO_LARGE', 'File requires more than 10000 multipart parts.');
        }
        const parts = await Promise.all(
          Array.from({ length: partCount }, async (_, index) => ({
            partNumber: index + 1,
            start: index * partSize,
            end: Math.min((index + 1) * partSize, file.size),
            url: await config.storage.presignPart({
              key,
              uploadId,
              partNumber: index + 1,
              expiresIn: uploadExpirySeconds,
            }),
          }))
        );
        upload = { kind: 'multipart', uploadId, parts };
      } catch (error) {
        await config.storage.abortMultipart({ key, uploadId }).catch(() => undefined);
        throw error;
      }
    } else {
      upload = {
        kind: 'put',
        url: await config.storage.presignPut({
          key,
          contentType: file.type,
          metadata: objectMetadata,
          expiresIn: uploadExpirySeconds,
        }),
        headers: {
          'Content-Type': file.type,
          ...Object.fromEntries(
            Object.entries(objectMetadata).map(([name, value]) => [`x-amz-meta-${name}`, value])
          ),
        },
      };
    }
    const sessionToken = createSessionToken(
      {
        version: 1,
        id,
        endpoint: body.endpoint,
        key,
        file,
        access,
        retention,
        expiresAt: expiresAt?.toISOString() ?? null,
        uploadExpiresAt,
        metadata,
        input: body.input,
        multipart,
      },
      config.secret
    );
    prepared.push({
      id,
      key,
      name: file.name,
      type: file.type,
      size: file.size,
      access,
      retention,
      expiresAt: expiresAt?.toISOString() ?? null,
      upload,
      sessionToken,
    });
  }
  return json(prepared);
};

const finalizeUpload = async <TRouter extends FileRouter>(
  body: RaildropFinalizeRequest,
  config: RaildropHandlerConfig<TRouter>
): Promise<Response> => {
  const session = readSessionToken(body.sessionToken, config.secret);
  if (session.endpoint !== body.endpoint) {
    throw new RaildropError('FORBIDDEN', 'Upload session does not match this endpoint.');
  }
  const route = getRoute(config.router, body.endpoint);
  if (session.multipart) {
    const existing = await config.storage.head(session.key);
    if (!existing) {
      const parts =
        body.parts?.map((part) => ({ ETag: part.etag, PartNumber: part.partNumber })) ?? [];
      if (parts.length === 0)
        throw new RaildropError('BAD_REQUEST', 'Multipart parts are required.');
      await config.storage.completeMultipart({
        key: session.key,
        uploadId: session.multipart.uploadId,
        parts,
      });
    }
  }
  const stored = await config.storage.head(session.key);
  if (!stored) throw new RaildropError('NOT_FOUND', 'Uploaded object was not found.');
  if (
    stored.size !== session.file.size ||
    stored.contentType !== session.file.type ||
    stored.metadata['raildrop-id'] !== session.id ||
    stored.metadata['raildrop-access'] !== session.access ||
    stored.metadata['raildrop-retention'] !== session.retention
  ) {
    throw new RaildropError('BAD_REQUEST', 'Uploaded object metadata does not match the session.');
  }
  const file = {
    id: session.id,
    key: session.key,
    access: session.access,
    retention: session.retention,
    publicUrl:
      session.access === 'public'
        ? `${config.publicBaseUrl.replace(/\/$/, '')}/${session.key}`
        : null,
    name: session.file.name,
    type: session.file.type,
    size: session.file.size,
    etag: stored.etag,
    expiresAt: session.expiresAt,
  } satisfies Omit<RaildropUploadedFile<never>, 'serverData'>;
  await route._def.validate({ file, stored, metadata: session.metadata });
  try {
    const serverData = await route._def.onUploadComplete({
      input: session.input,
      metadata: session.metadata,
      file,
    });
    return json({ ...file, serverData });
  } catch (error) {
    await route._def.onUploadError?.({
      error,
      input: session.input,
      metadata: session.metadata,
    });
    throw new RaildropError('CALLBACK_ERROR', 'Upload completed but its callback failed.', {
      cause: error,
    });
  }
};

const abortUpload = async <TRouter extends FileRouter>(
  body: RaildropAbortRequest,
  config: RaildropHandlerConfig<TRouter>
): Promise<Response> => {
  const session = readSessionToken(body.sessionToken, config.secret);
  if (session.endpoint !== body.endpoint) {
    throw new RaildropError('FORBIDDEN', 'Upload session does not match this endpoint.');
  }
  if (session.multipart && !(await config.storage.head(session.key))) {
    await config.storage.abortMultipart({
      key: session.key,
      uploadId: session.multipart.uploadId,
    });
  }
  return json({ ok: true });
};

export const createRouteHandler =
  <TRouter extends FileRouter>(config: RaildropHandlerConfig<TRouter>) =>
  async (req: Request): Promise<Response> => {
    try {
      if (req.method !== 'POST') return json({ error: { code: 'BAD_REQUEST' } }, 405);
      let body: unknown;
      try {
        body = await req.json();
      } catch (error) {
        throw new RaildropError('BAD_REQUEST', 'Raildrop request body must be valid JSON.', {
          cause: error,
        });
      }
      if (!isRecord(body)) throw new RaildropError('BAD_REQUEST', 'Invalid Raildrop request.');
      if (body.action === 'prepare') {
        return await prepareUploads(req, readPrepareRequest(body), config);
      }
      if (body.action === 'finalize') {
        return await finalizeUpload(
          readSessionRequest(body, 'finalize') as RaildropFinalizeRequest,
          config
        );
      }
      if (body.action === 'abort') {
        return await abortUpload(readSessionRequest(body, 'abort') as RaildropAbortRequest, config);
      }
      throw new RaildropError('BAD_REQUEST', 'Unknown Raildrop action.');
    } catch (error) {
      return errorResponse(error);
    }
  };
