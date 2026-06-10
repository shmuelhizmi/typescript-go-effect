// Plain-TSX bootstrap: everything past this file is EffectScript (.ets/.etsx)
// compiled by the fork's own tsgo via vite-plugin-ets.
import { createRoot } from "react-dom/client";
import { App } from "./App";
import "./ui/styles.css";

createRoot(document.getElementById("root")!).render(<App />);
