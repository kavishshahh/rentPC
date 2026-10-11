import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  output: "standalone",
  // Pages are fully dynamic (they render per-user data from the API); keep
  // the default rendering model rather than cacheComponents.
  turbopack: {
    rules: {
      "*.css": {
        loaders: ["@tailwindcss/turbopack"],
        as: "*.css",
      },
    },
  },
};

export default nextConfig;
