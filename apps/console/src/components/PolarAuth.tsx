import type { ReactNode } from "react";
import "./polar-auth.css";

export function PolarAuthBrand() {
  return <div className="polar-auth-brand" aria-label="门道控制台">
    <span className="polar-auth-mark" aria-hidden="true">门</span>
    <div><b>MENDAO</b><small>CONSOLE</small></div>
  </div>;
}

export function PolarAuthFrame({ children }: { children: ReactNode }) {
  return <div className="login-page polar-auth-page">
    <svg className="polar-auth-orbits" aria-hidden="true">
      {Array.from({ length: 7 }, (_, index) => <ellipse
        key={index}
        cx="50%"
        cy="120%"
        rx={`${30 + index * 12}%`}
        ry={`${40 + index * 10}%`}
        fill="none"
        stroke={`rgba(143,227,255,${0.12 - index * 0.012})`}
      />)}
    </svg>
    {children}
  </div>;
}
