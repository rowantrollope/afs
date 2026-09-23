import { Button } from "@redis-ui/components";
import { queryOptions, useQuery, useQueryClient } from "@tanstack/react-query";
import type { FormEvent, PropsWithChildren } from "react";
import { createContext, useContext, useEffect, useRef, useState } from "react";
import {
  Field,
  FormGrid,
  NoticeBody,
  NoticeCard,
  NoticeTitle,
  PageStack,
  SectionCard,
  TextInput,
} from "../components/afs-kit";
import { afsApi } from "./api/afs";
import { browserAuthApi } from "./api/browser-auth";
import { getSessionToken, setSessionToken } from "./api/session-token";
import type { AFSAuthConfig } from "./types/afs";

type AuthContextValue = {
  config: AFSAuthConfig;
  isLoading: boolean;
  isAuthenticated: boolean;
  isSignedOut: boolean;
  supportsAccountAuth: boolean;
  displayName: string;
  secondaryLabel: string;
  signOut: () => void;
};
const AuthContext = createContext<AuthContextValue | null>(null);

const authConfigQueryOptions = () =>
  queryOptions({
    queryKey: ["afs", "auth", "config"],
    queryFn: async () => {
      const config = await afsApi.getAuthConfig();
      const legacyToken = getSessionToken();
      if (!config.browserSessions || !legacyToken) return config;
      // Upgrade an older tab once, before rendering private routes. The server
      // cookie is HttpOnly; new sign-ins never store a token in JavaScript.
      setSessionToken("");
      // Another tab may already have established a valid cookie while this
      // tab retained an expired bearer. Recheck after removing that override.
      if (!config.authenticated) return afsApi.getAuthConfig();
      await browserAuthApi.createSession(legacyToken);
      return afsApi.getAuthConfig();
    },
    staleTime: Infinity,
    refetchOnWindowFocus: "always",
    retry: false,
  });

export function AuthProvider({ children }: PropsWithChildren) {
  const queryClient = useQueryClient();
  const [token, setToken] = useState("");
  const [signedOut, setSignedOut] = useState(false);
  const [pending, setPending] = useState(false);
  const [signInError, setSignInError] = useState("");
  const [signOutError, setSignOutError] = useState("");
  const submitting = useRef(false);
  const authQuery = useQuery(authConfigQueryOptions());
  const connectingCLI = window.location.pathname === "/connect-cli";
  async function signOut() {
    if (submitting.current) return;
    submitting.current = true;
    setSignOutError("");
    try {
      if (authQuery.data?.browserSessions) {
        await browserAuthApi.deleteSession();
      }
      setSignedOut(true);
      setSessionToken("");
      setToken("");
      setSignInError("");
      queryClient.clear();
      void authQuery.refetch();
    } catch (error) {
      setSignOutError(
        `Could not sign out. Your browser is still signed in. ${error instanceof Error ? error.message : "Please try again."}`,
      );
    } finally {
      submitting.current = false;
    }
  }
  useEffect(() => {
    const invalidate = () => {
      setSignedOut(true);
      setSessionToken("");
      queryClient.removeQueries({
        predicate: (query) => query.queryKey[1] !== "auth",
      });
      void queryClient.invalidateQueries({
        queryKey: authConfigQueryOptions().queryKey,
      });
    };
    window.addEventListener("afs:unauthorized", invalidate);
    return () => window.removeEventListener("afs:unauthorized", invalidate);
  }, [queryClient]);

  async function signIn(event: FormEvent) {
    event.preventDefault();
    if (submitting.current || !token.trim()) return;
    submitting.current = true;
    setPending(true);
    setSignInError("");
    try {
      const config = authQuery.data ?? await afsApi.getAuthConfig();
      if (config.browserSessions) {
        await browserAuthApi.createSession(token);
        setSessionToken("");
      } else {
        // Compatibility for older control planes without browser sessions.
        setSessionToken(token);
      }
      setToken("");
      const result = await authQuery.refetch();
      if (result.error) throw result.error;
      if (!result.data?.authenticated) throw new Error("The token was not accepted.");
      setSignedOut(false);
    } catch (error) {
      setSignedOut(true);
      setSessionToken("");
      setSignInError(error instanceof Error ? error.message : "Could not sign in.");
    } finally {
      submitting.current = false;
      setPending(false);
    }
  }
  const config = authQuery.data;
  // Gate the router itself: route loaders cannot issue requests before auth.
  // Keep the current URL, including a CLI request, intact through sign-in.
  if (
    signedOut ||
    !config ||
    (config.signInRequired && !config.authenticated)
  ) {
    return (
      <PageStack style={{ maxWidth: 560, paddingTop: "12vh" }}>
        <SectionCard>
          <NoticeTitle>Agent Filesystem</NoticeTitle>
          {authQuery.isPending ? (
            <NoticeBody>Connecting to the control plane…</NoticeBody>
          ) : (
            <FormGrid onSubmit={signIn}>
              {connectingCLI && <h1 style={{ fontSize: 24, marginBottom: 0 }}>Sign in to connect your CLI</h1>}
              <NoticeBody>
                Enter an API key or the team token for this control plane.{" "}
                {config?.browserSessions
                  ? "This browser stays signed in, so future CLI connections only need your approval."
                  : "It is saved for this browser tab."}
                {connectingCLI && " You’ll review the CLI request after signing in."}
              </NoticeBody>
              <Field>
                API key or team token
                <TextInput
                  aria-label="API key or team token"
                  type="password"
                  autoComplete="off"
                  value={token}
                  onChange={(event) => setToken(event.target.value)}
                  required
                />
              </Field>
              {(signInError || authQuery.error) && (
                <NoticeCard $tone="danger" role="alert">
                  <NoticeBody>{signInError || authQuery.error?.message}</NoticeBody>
                </NoticeCard>
              )}
              <Button
                type="submit"
                disabled={pending || authQuery.isFetching || !token.trim()}
              >
                {pending ? "Signing in…" : connectingCLI ? "Sign in and continue" : "Connect"}
              </Button>
            </FormGrid>
          )}
        </SectionCard>
      </PageStack>
    );
  }
  return (
    <AuthContext.Provider
      value={{
        config,
        isLoading: false,
        isAuthenticated: config.authenticated,
        isSignedOut: false,
        supportsAccountAuth: false,
        displayName: config.user?.name || "Operator",
        secondaryLabel: "Self-managed",
        signOut: () => void signOut(),
      }}
    >
      {signOutError && (
        <NoticeCard $tone="danger" role="alert">
          <NoticeBody>{signOutError}</NoticeBody>
          <Button variant="secondary-fill" onClick={() => void signOut()}>Retry sign out</Button>
        </NoticeCard>
      )}
      {children}
    </AuthContext.Provider>
  );
}

export function useAuthSession() {
  const context = useContext(AuthContext);
  if (!context)
    throw new Error("useAuthSession must be used inside AuthProvider.");
  return context;
}
