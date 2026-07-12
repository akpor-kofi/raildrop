import { encodeNamespace, isPublicObjectKey, namespace } from '../core';
import { GLOBAL_MANIFEST_KEY } from './global-assets';

import type { GlobalAssetManifest } from './global-assets';
import type { RaildropObjectBody, RaildropStorage, RaildropStoredObject } from './storage';

export interface PublicGatewayConfig {
  storage: RaildropStorage;
  prefix?: string;
}

const noStore = (body: string, status: number): Response =>
  new Response(body, {
    status,
    headers: {
      'Access-Control-Allow-Origin': '*',
      'Cache-Control': 'no-store',
      'Content-Type': 'text/plain; charset=utf-8',
      'Cross-Origin-Resource-Policy': 'cross-origin',
      'X-Content-Type-Options': 'nosniff',
    },
  });

const readKey = (pathname: string, prefix: string): string | null => {
  if (prefix && !pathname.startsWith(prefix)) return null;
  const encodedPath = pathname.slice(prefix.length).replace(/^\/+/, '');
  const encodedSegments = encodedPath.split('/');
  if (encodedSegments.some((segment) => !segment)) return null;
  const decodedSegments = encodedSegments.map((segment) => decodeURIComponent(segment));
  const normalizedSegments = namespace(...decodedSegments);
  if (normalizedSegments.some((segment, index) => segment !== decodedSegments[index])) return null;
  return encodeNamespace(normalizedSegments);
};

const unsafeInlineType = (contentType: string | null): boolean =>
  contentType === 'text/html' ||
  contentType === 'application/xhtml+xml' ||
  contentType === 'image/svg+xml' ||
  contentType === 'application/javascript' ||
  contentType === 'text/javascript';

const metadataDownloadName = (metadata: Record<string, string>): string | null => {
  const encoded = metadata['raildrop-name'];
  if (!encoded) return null;
  try {
    return Buffer.from(encoded, 'base64url').toString('utf8');
  } catch {
    return null;
  }
};

const bodyToText = async (body: BodyInit): Promise<string> => new Response(body).text();

const resolveAlias = async (storage: RaildropStorage, alias: string): Promise<string | null> => {
  if (!/^[a-z0-9][a-z0-9-]{0,119}$/.test(alias)) return null;
  const manifestObject = await storage.get(GLOBAL_MANIFEST_KEY);
  if (!manifestObject) return null;
  try {
    const manifest = JSON.parse(
      await bodyToText(manifestObject.body as BodyInit)
    ) as Partial<GlobalAssetManifest>;
    return manifest.version === 1 ? (manifest.assets?.[alias]?.key ?? null) : null;
  } catch {
    return null;
  }
};

export const createPublicGatewayHandler =
  ({ storage, prefix = '' }: PublicGatewayConfig) =>
  async (req: Request): Promise<Response> => {
    if (req.method !== 'GET' && req.method !== 'HEAD') return noStore('Method not allowed', 405);
    const url = new URL(req.url);
    let key: string;
    try {
      const parsed = readKey(url.pathname, prefix);
      if (!parsed) return noStore('Invalid asset path', 400);
      key = parsed;
    } catch {
      return noStore('Invalid asset path', 400);
    }
    const alias = /^global\/([^/]+)$/.exec(key)?.[1];
    if (alias) {
      const resolved = await resolveAlias(storage, alias);
      if (!resolved) return noStore('Not found', 404);
      key = resolved;
    }
    if (!isPublicObjectKey(key)) return noStore('Not found', 404);
    const range = req.headers.get('range') ?? undefined;
    let object: RaildropStoredObject | RaildropObjectBody | null;
    try {
      object =
        req.method === 'HEAD' && !range ? await storage.head(key) : await storage.get(key, range);
    } catch (error) {
      const status = (error as { $metadata?: { httpStatusCode?: number } }).$metadata
        ?.httpStatusCode;
      return status === 416
        ? noStore('Range not satisfiable', 416)
        : noStore('Asset storage unavailable', 503);
    }
    if (!object) return noStore('Not found', 404);
    if (object.metadata['raildrop-access'] && object.metadata['raildrop-access'] !== 'public') {
      return noStore('Not found', 404);
    }
    const expiresAt = object.metadata['raildrop-expires-at'];
    const expiresAtTimestamp = expiresAt ? Date.parse(expiresAt) : Number.NaN;
    if (expiresAt && (!Number.isFinite(expiresAtTimestamp) || expiresAtTimestamp <= Date.now())) {
      return noStore('Not found', 404);
    }
    const isTemporary =
      object.metadata['raildrop-retention'] === 'temporary' || key.startsWith('public/tmp/');
    const temporaryCacheControl =
      isTemporary && Number.isFinite(expiresAtTimestamp)
        ? `public, max-age=${Math.max(0, Math.floor((expiresAtTimestamp - Date.now()) / 1000))}, must-revalidate`
        : 'no-store';
    const headers = new Headers({
      'Accept-Ranges': 'bytes',
      'Access-Control-Allow-Origin': '*',
      'Cache-Control': alias
        ? 'public, max-age=300, stale-while-revalidate=86400'
        : isTemporary
          ? temporaryCacheControl
          : (object.cacheControl ?? 'public, max-age=31536000, immutable'),
      'Content-Length': String(object.size),
      'Content-Type': object.contentType ?? 'application/octet-stream',
      'Cross-Origin-Resource-Policy': 'cross-origin',
      'X-Content-Type-Options': 'nosniff',
    });
    if (object.etag) headers.set('ETag', `"${object.etag}"`);
    if (object.lastModified) headers.set('Last-Modified', object.lastModified.toUTCString());
    if (object.contentDisposition) headers.set('Content-Disposition', object.contentDisposition);
    else if (unsafeInlineType(object.contentType)) {
      const name = metadataDownloadName(object.metadata) ?? 'download';
      headers.set('Content-Disposition', `attachment; filename="${name.replace(/["\\]/g, '_')}"`);
    }
    if ('contentRange' in object && typeof object.contentRange === 'string') {
      headers.set('Content-Range', object.contentRange);
    }
    const ifNoneMatch = req.headers.get('if-none-match') ?? '';
    const etagMatches = object.etag
      ? ifNoneMatch
          .split(',')
          .map((value) => value.trim().replace(/^W\//, '').replace(/^"|"$/g, ''))
          .some((value) => value === '*' || value === object.etag)
      : false;
    const ifModifiedSince = req.headers.get('if-modified-since');
    const modifiedSinceTimestamp = ifModifiedSince ? Date.parse(ifModifiedSince) : Number.NaN;
    const notModifiedSince =
      Boolean(object.lastModified) &&
      Number.isFinite(modifiedSinceTimestamp) &&
      (object.lastModified?.getTime() ?? Number.POSITIVE_INFINITY) <= modifiedSinceTimestamp;
    if (ifNoneMatch ? etagMatches : notModifiedSince) {
      return new Response(null, { status: 304, headers });
    }
    return new Response(
      req.method === 'HEAD' ? null : 'body' in object ? (object.body as BodyInit) : null,
      {
        status: req.headers.has('range') ? 206 : 200,
        headers,
      }
    );
  };
