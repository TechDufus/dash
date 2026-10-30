import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "@/App";
import { startStream } from "@/api/stream";
import "@/styles/globals.css";

startStream();

const root = document.getElementById("root");
if (root) {
  createRoot(root).render(
    <StrictMode>
      <App />
    </StrictMode>,
  );
}
