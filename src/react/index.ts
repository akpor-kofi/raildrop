import { useCallback, useState } from 'react';

import { genUploader } from '../client';

import type { RaildropClientConfig, RaildropUploadOptions } from '../client';
import type { FileRouter, InferEndpointInput } from '../server/router';

export const generateReactHelpers = <TRouter extends FileRouter>(config: RaildropClientConfig) => {
  const uploader = genUploader<TRouter>(config);
  const useRaildrop = <TEndpoint extends keyof TRouter & string>(endpoint: TEndpoint) => {
    const [isUploading, setIsUploading] = useState(false);
    const startUpload = useCallback(
      async (options: RaildropUploadOptions<InferEndpointInput<TRouter, TEndpoint>>) => {
        setIsUploading(true);
        try {
          return await uploader.uploadFiles(endpoint, options);
        } finally {
          setIsUploading(false);
        }
      },
      [endpoint]
    );
    return { startUpload, isUploading };
  };
  return { ...uploader, useRaildrop };
};
