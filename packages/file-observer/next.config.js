/** @type {import('next').NextConfig} */
const basePath = process.env.FILE_OBSERVER_BASE_PATH || process.env.NEXT_PUBLIC_FILE_OBSERVER_BASE_PATH || '';

const nextConfig = {
  reactStrictMode: true,
  output: 'export',
  ...(basePath ? { basePath } : {}),
};

module.exports = nextConfig;
