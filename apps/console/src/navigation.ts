import type { LucideIcon } from "lucide-react";

export interface NavGroup {
  label: string;
  items: { path: string; label: string; icon: LucideIcon }[];
}
