export interface RpcCall {
  url: string;
  key: string;
  functionName: string;
  args: Record<string, unknown>;
}

export interface RpcError {
  code?: string;
  message: string;
}

export type RpcResult =
  | { data: unknown; error: null }
  | { data: null; error: RpcError };

export type RpcCaller = (call: RpcCall) => Promise<RpcResult>;

export type Fetcher = (
  input: RequestInfo | URL,
  init?: RequestInit,
) => Promise<Response>;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export async function callSupabaseRpc(
  call: RpcCall,
  fetcher: Fetcher = fetch,
): Promise<RpcResult> {
  let response: Response;
  try {
    response = await fetcher(
      `${call.url.replace(/\/+$/, "")}/rest/v1/rpc/${
        encodeURIComponent(call.functionName)
      }`,
      {
        method: "POST",
        headers: {
          apikey: call.key,
          authorization: `Bearer ${call.key}`,
          "content-type": "application/json",
        },
        body: JSON.stringify(call.args),
      },
    );
  } catch {
    return { data: null, error: { message: "rpc_request_failed" } };
  }

  let body: unknown = null;
  try {
    body = await response.json();
  } catch {
    body = null;
  }
  if (response.ok) {
    return { data: body, error: null };
  }

  return {
    data: null,
    error: {
      code: isRecord(body) && typeof body.code === "string"
        ? body.code
        : undefined,
      message: isRecord(body) && typeof body.message === "string"
        ? body.message
        : "rpc_request_failed",
    },
  };
}
