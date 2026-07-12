export type RaildropAccess = 'public' | 'private';
export type RaildropRetention = 'permanent' | 'temporary';
export type RaildropFileType = 'image' | 'video' | 'audio' | 'pdf' | 'text' | 'blob';

export interface RaildropFileRule {
  maxFileSize: number | `${number}${'KB' | 'MB' | 'GB'}`;
  maxFileCount?: number;
  mimeTypes?: readonly string[];
}

export type RaildropFileRules = Partial<Record<RaildropFileType, RaildropFileRule>> &
  Record<string, RaildropFileRule | undefined>;

export interface RaildropRequestedFile {
  name: string;
  size: number;
  type: string;
  lastModified?: number;
}

export interface RaildropUploadedFile<TServerData = unknown> {
  id: string;
  key: string;
  access: RaildropAccess;
  retention: RaildropRetention;
  publicUrl: string | null;
  name: string;
  type: string;
  size: number;
  etag: string | null;
  expiresAt: string | null;
  serverData: TServerData;
}

export interface RaildropPreparedPart {
  partNumber: number;
  start: number;
  end: number;
  url: string;
}

export interface RaildropPreparedFile {
  id: string;
  key: string;
  name: string;
  type: string;
  size: number;
  access: RaildropAccess;
  retention: RaildropRetention;
  expiresAt: string | null;
  upload:
    | { kind: 'put'; url: string; headers: Record<string, string> }
    | { kind: 'multipart'; uploadId: string; parts: RaildropPreparedPart[] };
  sessionToken: string;
}

export interface RaildropCompletedPart {
  partNumber: number;
  etag: string;
}

export interface RaildropPrepareRequest<TInput = unknown> {
  action: 'prepare';
  endpoint: string;
  input: TInput;
  files: RaildropRequestedFile[];
}

export interface RaildropFinalizeRequest {
  action: 'finalize';
  endpoint: string;
  sessionToken: string;
  parts?: RaildropCompletedPart[];
}

export interface RaildropAbortRequest {
  action: 'abort';
  endpoint: string;
  sessionToken: string;
}

export class RaildropError extends Error {
  constructor(
    public readonly code:
      | 'BAD_REQUEST'
      | 'FORBIDDEN'
      | 'NOT_FOUND'
      | 'TOO_LARGE'
      | 'INVALID_TYPE'
      | 'EXPIRED'
      | 'STORAGE_ERROR'
      | 'CALLBACK_ERROR',
    message: string,
    options?: ErrorOptions
  ) {
    super(message, options);
    this.name = 'RaildropError';
  }
}

const SIZE_PATTERN = /^(\d+(?:\.\d+)?)(KB|MB|GB)$/;
const SIZE_FACTORS = { KB: 1_000, MB: 1_000_000, GB: 1_000_000_000 } as const;

export const parseFileSize = (value: RaildropFileRule['maxFileSize']): number => {
  if (typeof value === 'number') {
    if (!Number.isSafeInteger(value) || value <= 0) {
      throw new RaildropError('BAD_REQUEST', 'File size limits must be positive integers.');
    }
    return value;
  }

  const match = SIZE_PATTERN.exec(value);
  if (!match) throw new RaildropError('BAD_REQUEST', `Invalid file size: ${value}`);
  const amount = Number(match[1]);
  const unit = match[2] as keyof typeof SIZE_FACTORS;
  return Math.floor(amount * SIZE_FACTORS[unit]);
};

const INVALID_NAMESPACE = /[\\/]/;
const hasControlCharacter = (value: string): boolean =>
  [...value].some((character) => {
    const code = character.charCodeAt(0);
    return code <= 31 || code === 127;
  });

export const namespace = (...segments: readonly string[]): string[] => {
  if (segments.length === 0) {
    throw new RaildropError('BAD_REQUEST', 'A namespace must contain at least one segment.');
  }
  return segments.map((segment) => {
    const value = segment.trim();
    if (
      !value ||
      value === '.' ||
      value === '..' ||
      INVALID_NAMESPACE.test(value) ||
      hasControlCharacter(value)
    ) {
      throw new RaildropError('BAD_REQUEST', `Invalid namespace segment: ${segment}`);
    }
    try {
      if (decodeURIComponent(value) !== value) {
        throw new RaildropError('BAD_REQUEST', 'Namespace segments must not be pre-encoded.');
      }
    } catch (error) {
      if (error instanceof RaildropError) throw error;
      throw new RaildropError('BAD_REQUEST', 'Namespace segment contains malformed encoding.');
    }
    return value;
  });
};

export const encodeNamespace = (segments: readonly string[]): string =>
  namespace(...segments)
    .map((segment) => encodeURIComponent(segment))
    .join('/');

export const isPublicObjectKey = (key: string): boolean =>
  key.startsWith('public/') && !key.includes('..') && !key.includes('\\');

export const sanitizeDownloadName = (name: string): string => {
  const normalized = [...name.normalize('NFKC')]
    .map((character) =>
      hasControlCharacter(character) || /["\\/]/.test(character) ? '_' : character
    )
    .join('')
    .trim();
  return normalized.slice(0, 180) || 'download';
};

export const mimeGroupFor = (type: string): RaildropFileType => {
  if (type.startsWith('image/')) return 'image';
  if (type.startsWith('video/')) return 'video';
  if (type.startsWith('audio/')) return 'audio';
  if (type === 'application/pdf') return 'pdf';
  if (type.startsWith('text/')) return 'text';
  return 'blob';
};
