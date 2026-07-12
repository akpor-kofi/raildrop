import {
  createCipheriv,
  createDecipheriv,
  createHash,
  createHmac,
  randomBytes,
  randomUUID,
  timingSafeEqual,
} from 'node:crypto';

import { RaildropError } from '../core';

import type { RaildropAccess, RaildropRequestedFile, RaildropRetention } from '../core';

export interface RaildropSessionPayload {
  version: 1;
  id: string;
  endpoint: string;
  key: string;
  file: RaildropRequestedFile;
  access: RaildropAccess;
  retention: RaildropRetention;
  expiresAt: string | null;
  uploadExpiresAt: number;
  metadataDigest: string;
  metadata: unknown;
  input: unknown;
  multipart: { uploadId: string } | null;
}

const toBase64Url = (value: string | Uint8Array): string =>
  Buffer.from(value).toString('base64url');

const sign = (encoded: string, secret: string): string =>
  toBase64Url(createHmac('sha256', secret).update(encoded).digest());

const metadataDigest = (metadata: unknown): string =>
  createHash('sha256').update(JSON.stringify({ metadata })).digest('base64url');

const encryptionKey = (secret: string): Buffer =>
  createHash('sha256').update('raildrop:session:encryption:v1\0').update(secret).digest();

const safeEqual = (actual: string, expected: string): boolean => {
  const actualBytes = Buffer.from(actual, 'base64url');
  const expectedBytes = Buffer.from(expected, 'base64url');
  return (
    actualBytes.byteLength === expectedBytes.byteLength &&
    timingSafeEqual(actualBytes, expectedBytes)
  );
};

export const createSessionToken = (
  payload: Omit<RaildropSessionPayload, 'metadataDigest'>,
  secret: string
): string => {
  if (secret.length < 32) throw new Error('RAILDROP_SECRET must contain at least 32 characters.');
  const iv = randomBytes(12);
  const cipher = createCipheriv('aes-256-gcm', encryptionKey(secret), iv);
  cipher.setAAD(Buffer.from('raildrop-session-v1'));
  const encrypted = Buffer.concat([
    cipher.update(
      JSON.stringify({ ...payload, metadataDigest: metadataDigest(payload.metadata) }),
      'utf8'
    ),
    cipher.final(),
  ]);
  const unsigned = `v1.${toBase64Url(iv)}.${toBase64Url(encrypted)}.${toBase64Url(
    cipher.getAuthTag()
  )}`;
  return `${unsigned}.${sign(unsigned, secret)}`;
};

export const readSessionToken = (token: string, secret: string): RaildropSessionPayload => {
  if (secret.length < 32) throw new Error('RAILDROP_SECRET must contain at least 32 characters.');
  const [version, encodedIv, encrypted, encodedTag, signature, extra] = token.split('.');
  if (
    version !== 'v1' ||
    !encodedIv ||
    !encrypted ||
    !encodedTag ||
    !signature ||
    extra ||
    !safeEqual(signature, sign(`${version}.${encodedIv}.${encrypted}.${encodedTag}`, secret))
  ) {
    throw new RaildropError('FORBIDDEN', 'Invalid upload session.');
  }
  try {
    const decipher = createDecipheriv(
      'aes-256-gcm',
      encryptionKey(secret),
      Buffer.from(encodedIv, 'base64url')
    );
    decipher.setAAD(Buffer.from('raildrop-session-v1'));
    decipher.setAuthTag(Buffer.from(encodedTag, 'base64url'));
    const parsed = JSON.parse(
      Buffer.concat([
        decipher.update(Buffer.from(encrypted, 'base64url')),
        decipher.final(),
      ]).toString('utf8')
    ) as Partial<RaildropSessionPayload>;
    if (parsed.version !== 1 || typeof parsed.uploadExpiresAt !== 'number') {
      throw new RaildropError('FORBIDDEN', 'Invalid upload session.');
    }
    if (Date.now() > parsed.uploadExpiresAt) {
      throw new RaildropError('EXPIRED', 'Upload session expired.');
    }
    if (
      typeof parsed.metadataDigest !== 'string' ||
      !safeEqual(parsed.metadataDigest, metadataDigest(parsed.metadata))
    ) {
      throw new RaildropError('FORBIDDEN', 'Invalid upload session.');
    }
    return parsed as RaildropSessionPayload;
  } catch (error) {
    if (error instanceof RaildropError) throw error;
    throw new RaildropError('FORBIDDEN', 'Invalid upload session.', { cause: error });
  }
};

export const createUploadId = (): string => randomUUID();
