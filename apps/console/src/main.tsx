import { createRoot } from "react-dom/client";
import { Entry } from "./Entry.js";
import { initializeTheme } from "./theme.js";
import "./styles.css";

initializeTheme();
createRoot(document.getElementById("root") as HTMLElement).render(<Entry />);
