import type { NextConfig } from "next";

// Keep browser requests same-origin and proxy them to the Go API.
const configuredApiUrl = process.env.API_URL?.replace(/\/$/, "");
if (process.env.NODE_ENV === "production" && !configuredApiUrl) {
  throw new Error("API_URL is required for production builds");
}
const API_URL = configuredApiUrl ?? "http://localhost:4000";

const nextConfig: NextConfig = {
  turbopack: {
    root: __dirname,
  },

  async rewrites() {
    return [
      {
        source: "/api/:path*",
        destination: `${API_URL}/v1/:path*`,
      },
    ];
  },
};

export default nextConfig;
