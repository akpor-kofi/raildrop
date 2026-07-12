export { generateReactHelpers as generateReactNativeHelpers } from '../react';
export type { RaildropClientConfig, RaildropClientFile, RaildropUploadOptions } from '../client';

import type { RaildropClientFile } from '../client';

export interface ExpoRaildropAsset {
  uri: string;
  name: string;
  mimeType?: string | null;
  lastModified?: number;
}

export const fileFromExpoAsset = async (asset: ExpoRaildropAsset): Promise<RaildropClientFile> => {
  const response = await fetch(asset.uri);
  if (!response.ok) throw new Error(`Could not read Expo file: ${response.status}`);
  const blob = await response.blob();
  const type = (asset.mimeType ?? blob.type) || 'application/octet-stream';
  const typedBlob = blob.type === type ? blob : blob.slice(0, blob.size, type);
  return Object.assign(typedBlob, { name: asset.name, lastModified: asset.lastModified });
};
