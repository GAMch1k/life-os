import { type NextRequest, NextResponse } from "next/server";

type RouteContext = { params: Promise<{ path: string[] }> };

const FORWARDED_REQUEST_HEADERS = ["accept", "content-type"] as const;
const FORWARDED_RESPONSE_HEADERS = ["content-type", "location"] as const;

export async function proxyServiceRequest(
  request: NextRequest,
  context: RouteContext,
  serviceUrl: string,
) {
  const { path } = await context.params;
  const target = new URL(path.join("/"), `${serviceUrl}/`);
  target.search = request.nextUrl.search;

  const headers = new Headers();
  for (const name of FORWARDED_REQUEST_HEADERS) {
    const value = request.headers.get(name);
    if (value) {
      headers.set(name, value);
    }
  }

  const init: RequestInit = {
    cache: "no-store",
    headers,
    method: request.method,
  };
  if (request.method !== "GET" && request.method !== "HEAD") {
    const body = await request.arrayBuffer();
    if (body.byteLength > 0) {
      init.body = body;
    }
  }

  try {
    const response = await fetch(target, init);
    const responseHeaders = new Headers();
    for (const name of FORWARDED_RESPONSE_HEADERS) {
      const value = response.headers.get(name);
      if (value) {
        responseHeaders.set(name, value);
      }
    }
    return new NextResponse(response.body, {
      headers: responseHeaders,
      status: response.status,
    });
  } catch {
    return NextResponse.json(
      {
        error: { code: "service_unavailable", message: "Service unavailable" },
      },
      { status: 503 },
    );
  }
}
