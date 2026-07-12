import { sha256 } from '@noble/hashes/sha2.js';

import { RaildropError } from '../core';

import type { RaildropCompletedPart, RaildropPreparedFile, RaildropUploadedFile } from '../core';
import type { FileRouter, InferEndpointInput, InferEndpointOutput } from '../server/router';

export interface RaildropClientFile extends Blob {
  name: string;
  lastModified?: number;
}

export interface RaildropUploadOptions<TInput> {
  files: RaildropClientFile[];
  input: TInput;
  signal?: AbortSignal;
  headers?: HeadersInit | (() => HeadersInit | Promise<HeadersInit>);
  onUploadBegin?: (args: { file: string }) => void;
  onUploadProgress?: (args: { file: string; progress: number }) => void;
  abortOnCancel?: boolean;
  /** Stable caller identity for this upload, such as an attachment id. */
  resumeKey?: string;
  /** Authorization/account scope for persisted resume state. */
  resumeScope?: string;
}

export interface RaildropResumeState {
  prepared: RaildropPreparedFile;
  completedParts: RaildropCompletedPart[];
  savedAt: number;
}

export interface RaildropResumeStore {
  get(key: string): Promise<RaildropResumeState | null>;
  set(key: string, value: RaildropResumeState): Promise<void>;
  remove(key: string): Promise<void>;
}

export interface RaildropClientConfig {
  url: string;
  fetch?: typeof globalThis.fetch;
  partConcurrency?: number;
  retryAttempts?: number;
  resumeStore?: RaildropResumeStore;
  resumeScope?: string | (() => string | Promise<string>);
}

const bytesToHex = (bytes: Uint8Array): string =>
  [...bytes].map((value) => value.toString(16).padStart(2, '0')).join('');

const throwIfAborted = (signal?: AbortSignal): void => {
  if (!signal?.aborted) return;
  const error = new Error('Upload aborted.');
  error.name = 'AbortError';
  throw error;
};

const createResumeStorageKey = async (args: {
  url: string;
  endpoint: string;
  input: unknown;
  file: RaildropClientFile;
  callerKey?: string;
  scope: string;
  signal?: AbortSignal;
}): Promise<string> => {
  const hasher = sha256.create();
  hasher.update(
    new TextEncoder().encode(
      JSON.stringify({
        version: 2,
        url: args.url,
        endpoint: args.endpoint,
        input: args.input,
        callerKey: args.callerKey ?? null,
        scope: args.scope,
        name: args.file.name,
        type: args.file.type,
        size: args.file.size,
        lastModified: args.file.lastModified ?? 0,
      })
    )
  );
  hasher.update(new Uint8Array([0]));
  throwIfAborted(args.signal);

  if (typeof args.file.stream === 'function') {
    const reader = args.file.stream().getReader();
    try {
      while (true) {
        throwIfAborted(args.signal);
        const chunk = await reader.read();
        if (chunk.done) break;
        hasher.update(chunk.value);
      }
    } finally {
      reader.releaseLock();
    }
  } else {
    hasher.update(new Uint8Array(await args.file.arrayBuffer()));
  }
  throwIfAborted(args.signal);
  return `raildrop:v2:${bytesToHex(hasher.digest())}`;
};

const defaultResumeStore = (): RaildropResumeStore | undefined => {
  if (typeof globalThis.localStorage === 'undefined') return undefined;
  return {
    get: (key) => {
      const value = globalThis.localStorage.getItem(key);
      if (!value) return Promise.resolve(null);
      try {
        return Promise.resolve(JSON.parse(value) as RaildropResumeState);
      } catch {
        globalThis.localStorage.removeItem(key);
        return Promise.resolve(null);
      }
    },
    set: (key, value) => {
      globalThis.localStorage.setItem(key, JSON.stringify(value));
      return Promise.resolve();
    },
    remove: (key) => {
      globalThis.localStorage.removeItem(key);
      return Promise.resolve();
    },
  };
};

const retry = async <T>(callback: () => Promise<T>, attempts: number): Promise<T> => {
  let lastError: unknown;
  for (let attempt = 0; attempt <= attempts; attempt += 1) {
    try {
      return await callback();
    } catch (error) {
      lastError = error;
      if (error instanceof Error && error.name === 'AbortError') throw error;
      if (
        error instanceof RaildropError &&
        error.code !== 'STORAGE_ERROR' &&
        error.code !== 'CALLBACK_ERROR'
      ) {
        throw error;
      }
      if (attempt === attempts) break;
      await new Promise((resolve) => setTimeout(resolve, Math.min(250 * 2 ** attempt, 2_000)));
    }
  }
  throw lastError;
};

const resolveHeaders = async (headers: RaildropUploadOptions<unknown>['headers']) =>
  new Headers(typeof headers === 'function' ? await headers() : headers);

const readResponse = async <T>(response: Response): Promise<T> => {
  const value = (await response.json()) as T | { error?: { code?: string; message?: string } };
  if (!response.ok) {
    const error = value as { error?: { code?: string; message?: string } };
    const code = error.error?.code;
    const resolvedCode =
      code === 'BAD_REQUEST' ||
      code === 'FORBIDDEN' ||
      code === 'NOT_FOUND' ||
      code === 'TOO_LARGE' ||
      code === 'INVALID_TYPE' ||
      code === 'EXPIRED' ||
      code === 'STORAGE_ERROR' ||
      code === 'CALLBACK_ERROR'
        ? code
        : 'STORAGE_ERROR';
    throw new RaildropError(
      resolvedCode,
      error.error?.message ?? `Raildrop request failed with ${response.status}.`
    );
  }
  return value as T;
};

const uploadPut = async (
  fetcher: typeof globalThis.fetch,
  file: RaildropClientFile,
  prepared: RaildropPreparedFile,
  options: RaildropUploadOptions<unknown>,
  config: {
    concurrency: number;
    retryAttempts: number;
    completedParts: RaildropCompletedPart[];
    onPartComplete: (parts: RaildropCompletedPart[]) => Promise<void>;
  }
): Promise<RaildropCompletedPart[] | undefined> => {
  const upload = prepared.upload;
  if (upload.kind === 'put') {
    await retry(async () => {
      const headers = new Headers(upload.headers);
      if (!headers.has('Content-Type')) headers.set('Content-Type', file.type);
      const response = await fetcher(upload.url, {
        method: 'PUT',
        body: file,
        signal: options.signal,
        headers,
      });
      if (!response.ok) throw new RaildropError('STORAGE_ERROR', 'Railway upload failed.');
    }, config.retryAttempts);
    options.onUploadProgress?.({ file: file.name, progress: 100 });
    return undefined;
  }
  const parts = [...config.completedParts];
  const completed = new Set(parts.map((part) => part.partNumber));
  const pending = upload.parts.filter((part) => !completed.has(part.partNumber));
  let cursor = 0;
  const worker = async () => {
    while (cursor < pending.length) {
      const part = pending[cursor++];
      if (!part) return;
      const uploaded = await retry(async () => {
        const response = await fetcher(part.url, {
          method: 'PUT',
          body: file.slice(part.start, part.end),
          signal: options.signal,
        });
        if (!response.ok) {
          throw new RaildropError('STORAGE_ERROR', 'Railway multipart upload failed.');
        }
        const etag = response.headers.get('etag');
        if (!etag) {
          throw new RaildropError('STORAGE_ERROR', 'Multipart response did not include ETag.');
        }
        return { partNumber: part.partNumber, etag };
      }, config.retryAttempts);
      parts.push(uploaded);
      parts.sort((a, b) => a.partNumber - b.partNumber);
      await config.onPartComplete(parts);
      options.onUploadProgress?.({
        file: file.name,
        progress: Math.round((parts.length / upload.parts.length) * 100),
      });
    }
  };
  await Promise.all(Array.from({ length: Math.min(config.concurrency, pending.length) }, worker));
  return parts;
};

export const genUploader = <TRouter extends FileRouter>({
  url,
  fetch: configuredFetch,
  partConcurrency = 3,
  retryAttempts = 2,
  resumeStore: configuredResumeStore,
  resumeScope: configuredResumeScope = '',
}: RaildropClientConfig) => {
  const fetcher = configuredFetch ?? globalThis.fetch;
  const resumeStore = configuredResumeStore ?? defaultResumeStore();
  const uploadFiles = async <TEndpoint extends keyof TRouter & string>(
    endpoint: TEndpoint,
    options: RaildropUploadOptions<InferEndpointInput<TRouter, TEndpoint>>
  ): Promise<RaildropUploadedFile<InferEndpointOutput<TRouter, TEndpoint>>[]> => {
    if (options.files.length === 0) {
      throw new RaildropError('BAD_REQUEST', 'At least one file is required.');
    }
    const headers = await resolveHeaders(options.headers);
    headers.set('Content-Type', 'application/json');
    const resumeScope =
      options.resumeScope ??
      (typeof configuredResumeScope === 'function'
        ? await configuredResumeScope()
        : configuredResumeScope);
    const resumableFiles: {
      file: RaildropClientFile;
      resumeKey: string | null;
      saved: RaildropResumeState | null;
    }[] = [];
    for (const file of options.files) {
      const resumeKey = resumeStore
        ? await createResumeStorageKey({
            url,
            endpoint,
            input: options.input,
            file,
            callerKey: options.resumeKey,
            scope: resumeScope,
            signal: options.signal,
          })
        : null;
      const saved = resumeKey ? await resumeStore?.get(resumeKey) : null;
      resumableFiles.push({
        file,
        resumeKey,
        saved: saved && Date.now() - saved.savedAt < 50 * 60 * 1000 ? saved : null,
      });
    }

    let entries = resumableFiles.map((item) => item.saved?.prepared);
    if (entries.some((entry) => !entry)) {
      for (const item of resumableFiles) {
        if (item.resumeKey && item.saved) await resumeStore?.remove(item.resumeKey);
      }
      const prepared = await retry(
        async () =>
          readResponse<RaildropPreparedFile[]>(
            await fetcher(url, {
              method: 'POST',
              headers,
              signal: options.signal,
              body: JSON.stringify({
                action: 'prepare',
                endpoint,
                input: options.input,
                files: options.files.map((file) => ({
                  name: file.name,
                  size: file.size,
                  type: file.type,
                  lastModified: file.lastModified,
                })),
              }),
            })
          ),
        Math.max(0, retryAttempts)
      );
      if (prepared.length !== options.files.length) {
        throw new RaildropError(
          'BAD_REQUEST',
          'Prepare response did not match the requested files.'
        );
      }
      entries = prepared;
      for (const [index, item] of resumableFiles.entries()) {
        const entry = prepared[index];
        if (item.resumeKey && entry) {
          await resumeStore?.set(item.resumeKey, {
            prepared: entry,
            completedParts: [],
            savedAt: Date.now(),
          });
        }
      }
    }

    const results: RaildropUploadedFile<InferEndpointOutput<TRouter, TEndpoint>>[] = [];
    for (const [index, item] of resumableFiles.entries()) {
      const { file, resumeKey, saved } = item;
      const entry = entries[index];
      if (!entry) throw new RaildropError('BAD_REQUEST', 'Prepare response was incomplete.');
      const completedParts = saved?.prepared === entry ? saved.completedParts : [];
      options.onUploadBegin?.({ file: file.name });
      try {
        const parts = await uploadPut(
          fetcher,
          file,
          entry,
          options as RaildropUploadOptions<unknown>,
          {
            concurrency: Math.max(1, partConcurrency),
            retryAttempts: Math.max(0, retryAttempts),
            completedParts,
            onPartComplete: async (completedParts) => {
              if (!resumeKey) return;
              await resumeStore?.set(resumeKey, {
                prepared: entry,
                completedParts,
                savedAt: Date.now(),
              });
            },
          }
        );
        results.push(
          await retry(
            async () =>
              readResponse(
                await fetcher(url, {
                  method: 'POST',
                  headers,
                  signal: options.signal,
                  body: JSON.stringify({
                    action: 'finalize',
                    endpoint,
                    sessionToken: entry.sessionToken,
                    parts,
                  }),
                })
              ),
            Math.max(0, retryAttempts)
          )
        );
        if (resumeKey) await resumeStore?.remove(resumeKey);
      } catch (error) {
        if (options.signal?.aborted && options.abortOnCancel && entry.upload.kind === 'multipart') {
          await fetcher(url, {
            method: 'POST',
            headers,
            body: JSON.stringify({ action: 'abort', endpoint, sessionToken: entry.sessionToken }),
          }).catch(() => undefined);
          if (resumeKey) await resumeStore?.remove(resumeKey);
        }
        throw error;
      }
    }
    return results;
  };
  return { uploadFiles };
};
