import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Match the ecosystem convention (see benitogf/mono): author components in
// `.js` files containing JSX, and let esbuild treat them as JSX.
export default defineConfig({
    plugins: [
        react({
            include: '**/*.{jsx,js}',
        }),
    ],
    esbuild: {
        loader: 'jsx',
        include: /src\/.*\.js$/,
        exclude: [],
    },
    optimizeDeps: {
        esbuildOptions: {
            loader: {
                '.js': 'jsx',
            },
        },
    },
    server: {
        port: 3000,
        open: true,
    },
    build: {
        outDir: 'build',
        sourcemap: true,
    },
})
