import { Monitor, Moon, Sun } from "lucide-react";
import { setThemePreference, useTheme, type ThemePreference } from "../theme.js";
import { Glyph } from "./Icon.js";

const OPTIONS = [
  { value: "system", label: "系统", title: "跟随系统", icon: Monitor },
  { value: "light", label: "浅色", title: "浅色模式", icon: Sun },
  { value: "dark", label: "深色", title: "深色模式", icon: Moon },
] satisfies { value: ThemePreference; label: string; title: string; icon: typeof Monitor }[];

export function ThemePicker() {
  const { preference } = useTheme();
  return (
    <div className="theme-picker" role="group" aria-label="界面主题">
      {OPTIONS.map((option) => (
        <button
          key={option.value}
          type="button"
          className="theme-option"
          aria-label={option.title}
          aria-pressed={preference === option.value}
          title={option.title}
          onClick={() => setThemePreference(option.value)}
        >
          <Glyph icon={option.icon} size={13} />
          <span>{option.label}</span>
        </button>
      ))}
    </div>
  );
}
