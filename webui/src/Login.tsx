import { useState, type FormEvent } from "react";
import { api } from "./api";
import { Button, Icon } from "./components/ui";

export function Login({ onSignedIn, passwordSet }: { onSignedIn: () => void; passwordSet: boolean }) {
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [show, setShow] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!password || submitting) return;
    setSubmitting(true);
    setError("");
    try {
      await api.login(password);
      setPassword("");
      onSignedIn();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="login">
      <form className="login-card" onSubmit={submit}>
        <div className="login-brand">
          <span className="brand-mark large" aria-hidden="true">
            ◆
          </span>
          <h1>webport</h1>
          <p className="muted">Development dashboard</p>
        </div>
        {passwordSet ? (
          <>
            <label className="login-label" htmlFor="password">
              Password
            </label>
            <div className="password-input">
              <input
                id="password"
                type={show ? "text" : "password"}
                autoComplete="current-password"
                autoFocus
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                aria-invalid={Boolean(error)}
                aria-describedby={error ? "login-error" : undefined}
              />
              <button type="button" className="reveal" onClick={() => setShow((value) => !value)} aria-label={show ? "Hide password" : "Show password"}>
                <Icon name={show ? "eyeOff" : "eye"} />
              </button>
            </div>
            {error && (
              <p className="login-error" id="login-error" role="alert">
                {error}
              </p>
            )}
            <Button type="submit" variant="primary" disabled={!password || submitting}>
              {submitting ? "Signing in…" : "Sign in"}
            </Button>
          </>
        ) : (
          <p className="login-error" role="alert">
            No password is set. Run <code>webport webui password</code> on the host.
          </p>
        )}
        <p className="muted small login-foot">
          Change the password with <code>webport webui password</code>.
        </p>
      </form>
    </div>
  );
}
