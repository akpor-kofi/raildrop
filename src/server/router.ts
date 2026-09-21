import type {
  RaildropAccess,
  RaildropFileRules,
  RaildropRequestedFile,
  RaildropRetention,
  RaildropUploadedFile,
} from '../core';
import type { RaildropStoredObject } from './storage';

export interface RaildropPolicyOverride {
  access?: RaildropAccess;
  retention?: RaildropRetention;
  temporaryTtlSeconds?: number;
}

export const withRaildropPolicy = <TMetadata extends Record<string, unknown>>(
  metadata: TMetadata,
  policy: RaildropPolicyOverride
): TMetadata & { $raildrop: RaildropPolicyOverride } => ({ ...metadata, $raildrop: policy });

export interface RaildropMiddlewareArgs<TInput> {
  req: Request;
  input: TInput;
  files: RaildropRequestedFile[];
}

export interface RaildropNamespaceArgs<TInput, TMetadata> {
  input: TInput;
  metadata: TMetadata;
  file: RaildropRequestedFile;
}

export interface RaildropCompletionArgs<TInput, TMetadata> {
  input: TInput;
  metadata: TMetadata;
  file: Omit<RaildropUploadedFile<never>, 'serverData'>;
}

export interface RaildropRouteDefinition<TInput, TMetadata, TOutput> {
  rules: RaildropFileRules;
  access: RaildropAccess;
  retention: RaildropRetention;
  temporaryTtlSeconds: number;
  middleware: (args: RaildropMiddlewareArgs<TInput>) => Promise<TMetadata>;
  namespace: (args: RaildropNamespaceArgs<TInput, TMetadata>) => readonly string[];
  onUploadComplete: (args: RaildropCompletionArgs<TInput, TMetadata>) => Promise<TOutput>;
  validate: (args: {
    file: Omit<RaildropUploadedFile<never>, 'serverData'>;
    stored: RaildropStoredObject;
    metadata: TMetadata;
  }) => Promise<void>;
  onUploadError?: (args: { error: unknown; input: TInput; metadata: TMetadata }) => Promise<void>;
}

export interface RaildropRoute<TInput = unknown, TMetadata = unknown, TOutput = unknown> {
  readonly _def: RaildropRouteDefinition<TInput, TMetadata, TOutput>;
  readonly $types?: { input: TInput; metadata: TMetadata; output: TOutput };
}

// Route metadata and callback output are intentionally erased at the router boundary;
// endpoint-specific inference is retained by the concrete router object.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type FileRouter = Record<string, RaildropRoute<any, any, any>>;

export type InferEndpointInput<TRouter extends FileRouter, TEndpoint extends keyof TRouter> =
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  TRouter[TEndpoint] extends RaildropRoute<infer TInput, any, any> ? TInput : never;

export type InferEndpointOutput<TRouter extends FileRouter, TEndpoint extends keyof TRouter> =
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  TRouter[TEndpoint] extends RaildropRoute<any, any, infer TOutput> ? TOutput : never;

export class RaildropRouteBuilder<
  TInput = unknown,
  TMetadata = Record<string, never>,
  TOutput = void,
> implements RaildropRoute<TInput, TMetadata, TOutput> {
  readonly $types?: { input: TInput; metadata: TMetadata; output: TOutput };

  constructor(readonly _def: RaildropRouteDefinition<TInput, TMetadata, TOutput>) {}

  input<TNextInput>(): RaildropRouteBuilder<TNextInput, TMetadata, TOutput> {
    return this as unknown as RaildropRouteBuilder<TNextInput, TMetadata, TOutput>;
  }

  access(value: RaildropAccess): RaildropRouteBuilder<TInput, TMetadata, TOutput> {
    return new RaildropRouteBuilder({ ...this._def, access: value });
  }

  retention(
    value: RaildropRetention,
    options?: { ttlSeconds?: number }
  ): RaildropRouteBuilder<TInput, TMetadata, TOutput> {
    return new RaildropRouteBuilder({
      ...this._def,
      retention: value,
      temporaryTtlSeconds: options?.ttlSeconds ?? this._def.temporaryTtlSeconds,
    });
  }

  middleware<TNextMetadata>(
    callback: (args: RaildropMiddlewareArgs<TInput>) => Promise<TNextMetadata> | TNextMetadata
  ): RaildropRouteBuilder<TInput, TNextMetadata, TOutput> {
    return new RaildropRouteBuilder({
      ...this._def,
      middleware: async (args: RaildropMiddlewareArgs<TInput>) => callback(args),
    } as unknown as RaildropRouteDefinition<TInput, TNextMetadata, TOutput>);
  }

  namespace(
    callback: (args: RaildropNamespaceArgs<TInput, TMetadata>) => readonly string[]
  ): RaildropRouteBuilder<TInput, TMetadata, TOutput> {
    return new RaildropRouteBuilder({ ...this._def, namespace: callback });
  }

  onUploadComplete<TNextOutput>(
    callback: (
      args: RaildropCompletionArgs<TInput, TMetadata>
    ) => Promise<TNextOutput> | TNextOutput
  ): RaildropRouteBuilder<TInput, TMetadata, TNextOutput> {
    return new RaildropRouteBuilder({
      ...this._def,
      onUploadComplete: async (args) => callback(args),
    });
  }

  validate(
    callback: (args: {
      file: Omit<RaildropUploadedFile<never>, 'serverData'>;
      stored: RaildropStoredObject;
      metadata: TMetadata;
    }) => Promise<void> | void
  ): RaildropRouteBuilder<TInput, TMetadata, TOutput> {
    return new RaildropRouteBuilder({ ...this._def, validate: async (args) => callback(args) });
  }

  onUploadError(
    callback: (args: { error: unknown; input: TInput; metadata: TMetadata }) => Promise<void> | void
  ): RaildropRouteBuilder<TInput, TMetadata, TOutput> {
    return new RaildropRouteBuilder({
      ...this._def,
      onUploadError: async (args) => callback(args),
    });
  }
}

export const createRaildrop =
  () =>
  (rules: RaildropFileRules): RaildropRouteBuilder =>
    new RaildropRouteBuilder({
      rules,
      access: 'public',
      retention: 'permanent',
      temporaryTtlSeconds: 7 * 24 * 60 * 60,
      middleware: () => Promise.resolve({}),
      namespace: () => ['uploads'],
      validate: () => Promise.resolve(),
      onUploadComplete: () => Promise.resolve(undefined),
    });
