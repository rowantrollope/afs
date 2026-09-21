import { Button } from "@redis-ui/components";
import { queryOptions, useQuery, useQueryClient } from "@tanstack/react-query";
import type { FormEvent, PropsWithChildren } from "react";
import { createContext, useContext, useEffect, useState } from "react";
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
import { setSessionToken } from "./api/session-token";
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
    queryFn: () => afsApi.getAuthConfig(),
    staleTime: Infinity,
    retry: false,
  });

export function AuthProvider({ children }: PropsWithChildren) {
  const queryClient = useQueryClient();
  const [token, setToken] = useState("");
  const [submitted, setSubmitted] = useState(false);
  const [signedOut, setSignedOut] = useState(false);
  const authQuery = useQuery(authConfigQueryOptions());
  const signOut = () => {
    setSignedOut(true);
    setSessionToken("");
    setToken("");
    setSubmitted(false);
    queryClient.clear();
    void authQuery.refetch();
  };
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
    setSessionToken(token);
    setSubmitted(true);
    const result = await authQuery.refetch();
    setSignedOut(!!result.error || !result.data?.authenticated);
    setToken("");
  }
  const config = authQuery.data;
  // Gate the router itself: route loaders cannot issue requests before auth.
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
              <NoticeBody>
                Enter an API key or the team token for this control plane. It is
                saved for this browser tab.
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
              {(authQuery.error || (submitted && !config?.authenticated)) && (
                <NoticeCard $tone="danger" role="alert">
                  <NoticeBody>
                    {authQuery.error?.message ?? "The token was not accepted."}
                  </NoticeBody>
                </NoticeCard>
              )}
              <Button
                type="submit"
                disabled={authQuery.isFetching || !token.trim()}
              >
                Connect
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
        signOut,
      }}
    >
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
