/** @type {import('next').NextConfig} */
const nextConfig = {
  // Emit a fully static site into ./out — no Node server at runtime. The Go
  // binary embeds ./out and serves it under /dashboard.
  output: "export",

  // Everything (the HTML entry and every _next/* asset) is served beneath the
  // /dashboard route. basePath rewrites internal links; assetPrefix rewrites
  // asset URLs — both must be /dashboard.
  basePath: "/dashboard",
  assetPrefix: "/dashboard",

  // No Next.js image optimizer at runtime (there's no server).
  images: { unoptimized: true },

  // Emit /dashboard/foo/index.html rather than /dashboard/foo.html, so a plain
  // static file server (our Go wildcard handler) can resolve directory-style
  // paths.
  trailingSlash: true,
};

module.exports = nextConfig;
