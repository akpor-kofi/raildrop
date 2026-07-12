import type { Context, Hono } from 'hono';

export const registerRaildropHandler = (
  app: Hono,
  path: string,
  handler: (request: Request) => Promise<Response>
) => {
  app.all(path, (context: Context) => handler(context.req.raw));
  return app;
};
