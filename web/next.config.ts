import type { NextConfig } from "next";

// Keep browser requests same-origin and proxy them to the Go API.
const API_URL =
  process.env.NODE_ENV === "production"
    ? "https://macro-max-api.onrender.com"
    : (process.env.API_URL ?? "http://localhost:4000");

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
