import { createContext, useContext, type ReactNode } from "react";
import type { ConsoleTeam, ConsoleUser, Membership } from "./permissions.js";

export interface AuthState {
  user: ConsoleUser;
  memberships: Membership[];
  teams: ConsoleTeam[];
  teamId: string;
  team: ConsoleTeam | undefined;
  membership: Membership | undefined;
  isRoot: boolean;
  canManageTeam: boolean;
  selectTeam: (id: string) => void;
  refreshSession: () => Promise<void>;
}

const AuthContext = createContext<AuthState | null>(null);
export function AuthProvider({
  value,
  children,
}: {
  value: AuthState;
  children: ReactNode;
}) {
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}
export function useAuth(): AuthState {
  const state = useContext(AuthContext);
  if (!state) throw new Error("Console authentication context is missing");
  return state;
}
