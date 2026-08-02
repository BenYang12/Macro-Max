import type { NextConfig } from "next";

// THE CORS SOLUTION, and the reason I don't have to touch the Go server.
//
// A browser blocks a fetch from localhost:3000 to localhost:4000 unless the
// server sends Access-Control-Allow-Origin headers — and mine sends none.
// I have two ways out: write CORS middleware in Go, or make the request
// same-origin so the rule never applies.
//
// A rewrite does the second. The browser asks Next for /api/solve, Next
// forwards it server-to-server to the Go API, and the response comes back from
// the origin the page was loaded from. No preflight, no headers, no Go change.
// Server-to-server calls aren't subject to the same-origin policy at all — that
// policy is a BROWSER rule, not an HTTP one.
//
// The bonus is that my frontend never hardcodes the API's address. In Phase 7
// this env var points at Fly instead of localhost and nothing else changes.
const API_URL = process.env.API_URL ?? "http://localhost:4000";

const nextConfig: NextConfig = {
  // Pin the workspace root. Without this, Turbopack walks UP looking for a
  // lockfile and found a stray package-lock.json in my home directory, then
  // treated that as the project root. Saying so explicitly is more robust than
  // deleting someone's unrelated file.
  turbopack: {
    root: __dirname,
  },

  async rewrites() {
    return [
      {
        // /api/solve -> http://localhost:4000/v1/solve
        // I strip my own /api prefix and add the server's /v1, so the version
        // lives in exactly one place instead of being repeated at every call.
        source: "/api/:path*",
        destination: `${API_URL}/v1/:path*`,
      },
    ];
  },
};

export default nextConfig;
