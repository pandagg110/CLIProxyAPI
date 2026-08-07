import {
  constantTimeEqual,
  IngestRpcResponse,
  readBearerToken,
  sha256Hex,
  validateIngestPayload,
} from "../_shared/contracts.ts";
import { callSupabaseRpc, RpcCaller } from "../_shared/rpc.ts";

const corsHeaders = {
  "access-control-allow-origin": "*",
  "access-control-allow-headers": "authorization, content-type",
  "access-control-allow-methods": "POST, OPTIONS",
};

type EnvReader = (name: string) => string | undefined;

export interface IngestHandlerDependencies {
  env?: EnvReader;
  rpc?: RpcCaller;
  compareTokens?: (left: string, right: string) => Promise<boolean>;
  hashBody?: (body: string) => Promise<string>;
}

function jsonResponse(
  body: unknown,
  status: number,
  headers: HeadersInit = {},
): Response {
  return Response.json(body, {
    status,
    headers: {
      ...corsHeaders,
      "cache-control": "no-store",
      ...headers,
    },
  });
}

export function createIngestHandler(
  dependencies: IngestHandlerDependencies = {},
): (request: Request) => Promise<Response> {
  const env = dependencies.env ?? ((name: string) => Deno.env.get(name));
  const rpc = dependencies.rpc ?? callSupabaseRpc;
  const compareTokens = dependencies.compareTokens ?? constantTimeEqual;
  const hashBody = dependencies.hashBody ?? sha256Hex;

  return async (request: Request): Promise<Response> => {
    if (request.method === "OPTIONS") {
      return new Response(null, { status: 204, headers: corsHeaders });
    }
    if (request.method !== "POST") {
      return jsonResponse(
        { error: "method_not_allowed" },
        405,
        { allow: "POST, OPTIONS" },
      );
    }

    const expectedToken = env("LOG_STATS_INGEST_TOKEN");
    if (!expectedToken) {
      return jsonResponse({ error: "server_misconfigured" }, 500);
    }
    const providedToken = readBearerToken(request.headers.get("authorization"));
    if (
      providedToken === null ||
      !(await compareTokens(providedToken, expectedToken))
    ) {
      return jsonResponse({ error: "unauthorized" }, 401);
    }

    const rawBody = await request.text();
    let parsedBody: unknown;
    try {
      parsedBody = JSON.parse(rawBody);
    } catch {
      return jsonResponse({ error: "invalid_json" }, 400);
    }

    const validation = validateIngestPayload(parsedBody);
    if (!validation.ok) {
      return jsonResponse(
        { error: "validation_error", message: validation.error },
        422,
      );
    }

    const supabaseURL = env("SUPABASE_URL");
    const serviceRoleKey = env("SUPABASE_SERVICE_ROLE_KEY");
    if (!supabaseURL || !serviceRoleKey) {
      return jsonResponse({ error: "server_misconfigured" }, 500);
    }

    const result = await rpc({
      url: supabaseURL,
      key: serviceRoleKey,
      functionName: "ingest_log_usage_v1",
      args: {
        payload: validation.value,
        payload_sha256: await hashBody(rawBody),
      },
    });
    if (result.error === null) {
      if (result.data === null || typeof result.data !== "object") {
        return jsonResponse({ error: "ingest_failed" }, 502);
      }
      return jsonResponse(result.data as IngestRpcResponse, 200);
    }

    if (result.error.message.includes("event_id_conflict")) {
      return jsonResponse({ error: "event_id_conflict" }, 409);
    }
    if (
      result.error.code?.startsWith("22") ||
      result.error.message.startsWith("validation_error:")
    ) {
      return jsonResponse(
        {
          error: "validation_error",
          message: "payload failed database validation",
        },
        422,
      );
    }
    return jsonResponse({ error: "ingest_failed" }, 502);
  };
}

if (import.meta.main) {
  Deno.serve(createIngestHandler());
}
