import { createRoot } from "react-dom/client";
import { Entry } from "./Entry.js";
import "./styles.css";

createRoot(document.getElementById("root") as HTMLElement).render(<Entry />);
