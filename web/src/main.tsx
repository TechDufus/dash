import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@/styles/globals.css";

const root = document.getElementById("root");
if (root) {
  createRoot(root).render(
    <StrictMode>
      <div>dash</div>
    </StrictMode>,
  );
}
