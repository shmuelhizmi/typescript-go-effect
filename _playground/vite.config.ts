import { defineConfig } from "vite";
import { ets } from "./vite/vite-plugin-ets";

export default defineConfig({
    plugins: [ets()],
    server: {
        // The run sandbox iframe has an opaque origin (sandbox="allow-scripts");
        // module fetches from it need CORS.
        cors: true,
    },
});
