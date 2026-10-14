import { useCallback, useEffect, useState } from "react";
import { api } from "./api";
import { Dashboard } from "./Dashboard";
import { useTheme } from "./hooks";
import { Login } from "./Login";

type AuthState = { status: "checking" } | { status: "signed-out"; passwordSet: boolean; error?: string } | { status: "signed-in" };

export function App() {
  const [auth, setAuth] = useState<AuthState>({ status: "checking" });
  const [theme, cycleTheme] = useTheme();

  const check = useCallback(async () => {
    try {
      const result = await api.auth();
      setAuth(result.authenticated ? { status: "signed-in" } : { status: "signed-out", passwordSet: result.password_set });
    } catch (error) {
      setAuth({ status: "signed-out", passwordSet: true, error: error instanceof Error ? error.message : String(error) });
    }
  }, []);

  useEffect(() => {
    void check();
  }, [check]);

  if (auth.status === "checking") {
    return (
      <div className="login">
        <div className="spinner" aria-label="Loading" />
      </div>
    );
  }
  if (auth.status === "signed-out") {
    return <Login passwordSet={auth.passwordSet} onSignedIn={() => setAuth({ status: "signed-in" })} />;
  }
  return <Dashboard theme={theme} cycleTheme={cycleTheme} onSignedOut={() => setAuth({ status: "signed-out", passwordSet: true })} />;
}
