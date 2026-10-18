import createClient from "openapi-fetch";
import type { paths } from "./schema.d.ts";

// Type-safe API client generated directly from OpenAPI 3.0 specification
export const api = createClient<paths>({
  baseUrl: "",
});

export type { paths };
