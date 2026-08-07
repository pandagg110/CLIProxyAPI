import {
  parseDailyQuery,
  PublicDailyUsageResponse,
} from "../_shared/contracts.ts";
import { callSupabaseRpc, RpcCaller } from "../_shared/rpc.ts";
import { dashboardHtml } from "./dashboard_html.ts";

const corsHeaders = {
  "access-control-allow-origin": "*",
  "access-control-allow-headers": "authorization, content-type",
  "access-control-allow-methods": "GET, OPTIONS",
};

type EnvReader = (name: string) => string | undefined;

export interface DashboardHandlerDependencies {
  env?: EnvReader;
  rpc?: RpcCaller;
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
      ...headers,
    },
  });
}

export function createDashboardHandler(
  dependencies: DashboardHandlerDependencies = {},
): (request: Request) => Promise<Response> {
  const env = dependencies.env ?? ((name: string) => Deno.env.get(name));
  const rpc = dependencies.rpc ?? callSupabaseRpc;

  return async (request: Request): Promise<Response> => {
    if (request.method === "OPTIONS") {
      return new Response(null, { status: 204, headers: corsHeaders });
    }
    if (request.method !== "GET") {
      return jsonResponse(
        { error: "method_not_allowed" },
        405,
        { allow: "GET, OPTIONS", "cache-control": "no-store" },
      );
    }

    const url = new URL(request.url);
    if (url.searchParams.get("api") !== "daily") {
      return new Response(dashboardHtml, {
        status: 200,
        headers: {
          ...corsHeaders,
          "cache-control": "no-store",
          "content-type": "text/html; charset=utf-8",
        },
      });
    }

    const query = parseDailyQuery(url);
    if (!query.ok) {
      return jsonResponse(
        { error: "invalid_query", message: query.error },
        400,
        { "cache-control": "no-store" },
      );
    }

    const supabaseURL = env("SUPABASE_URL");
    const anonKey = env("SUPABASE_ANON_KEY");
    if (!supabaseURL || !anonKey) {
      return jsonResponse(
        { error: "server_misconfigured" },
        500,
        { "cache-control": "no-store" },
      );
    }

    const result = await rpc({
      url: supabaseURL,
      key: anonKey,
      functionName: "get_public_daily_usage",
      args: {
        p_from: query.value.from,
        p_to: query.value.to,
        p_search: query.value.search,
        p_page: query.value.page,
        p_page_size: query.value.pageSize,
      },
    });
    if (result.error !== null || result.data === null) {
      return jsonResponse(
        { error: "dashboard_unavailable" },
        502,
        { "cache-control": "no-store" },
      );
    }

    return jsonResponse(
      result.data as PublicDailyUsageResponse,
      200,
      { "cache-control": "public, max-age=30, stale-while-revalidate=60" },
    );
  };
}

if (import.meta.main) {
  Deno.serve(createDashboardHandler());
}
