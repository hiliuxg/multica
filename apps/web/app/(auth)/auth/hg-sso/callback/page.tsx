"use client";

import { Suspense, useEffect, useRef, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { sanitizeNextUrl, useAuthStore } from "@multica/core/auth";
import { api } from "@multica/core/api";
import { paths, resolvePostAuthDestination } from "@multica/core/paths";
import { useWorkspaceList } from "@multica/core/workspace";
import { redirectToCliCallback, validateCliCallback } from "@multica/views/auth";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@multica/ui/components/ui/card";
import { Button } from "@multica/ui/components/ui/button";
import { Loader2, RefreshCw } from "lucide-react";
import { useT } from "@multica/views/i18n";

function TMEOACallbackContent() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const { t } = useT("auth");
  const user = useAuthStore((state) => state.user);
  const isLoading = useAuthStore((state) => state.isLoading);
  const gatewayError = searchParams.get("error") === "authentication_failed";
  const cliCallbackRaw = searchParams.get("cli_callback");
  const cliState = searchParams.get("cli_state") ?? "";
  const isDesktopHandoff =
    searchParams.get("platform") === "desktop" && cliCallbackRaw === null;
  const { workspaces, ready, unavailable } = useWorkspaceList({
    enabled:
      user !== null && cliCallbackRaw === null && !isDesktopHandoff,
  });
  const redirectedRef = useRef(false);
  const [handoffFailed, setHandoffFailed] = useState(false);
  const [cliAuthorizing, setCliAuthorizing] = useState(false);

  useEffect(() => {
    if (
      gatewayError ||
      handoffFailed ||
      redirectedRef.current ||
      isLoading ||
      !user
    ) {
      return;
    }

    if (isDesktopHandoff) {
      redirectedRef.current = true;
      void api
        .issueCliToken()
        .then(({ token }) => {
          window.location.href = `multica://auth/callback?token=${encodeURIComponent(token)}`;
        })
        .catch(() => {
          setHandoffFailed(true);
        });
      return;
    }

    if (cliCallbackRaw) {
      return;
    }

    if (user.onboarded_at == null) {
      redirectedRef.current = true;
      router.replace(paths.onboarding());
      return;
    }

    if (!ready) {
      return;
    }
    redirectedRef.current = true;
    const nextUrl = sanitizeNextUrl(searchParams.get("next"));
    if (user.onboarded_at != null && nextUrl) {
      router.replace(nextUrl);
      return;
    }
    router.replace(
      resolvePostAuthDestination(workspaces, user.onboarded_at != null),
    );
  }, [
    cliCallbackRaw,
    cliState,
    gatewayError,
    handoffFailed,
    isDesktopHandoff,
    isLoading,
    ready,
    router,
    searchParams,
    user,
    workspaces,
  ]);

  const failed =
    gatewayError ||
    handoffFailed ||
    (cliCallbackRaw !== null && !validateCliCallback(cliCallbackRaw)) ||
    (!isLoading && user === null) ||
    unavailable;
  const validCliCallback =
    cliCallbackRaw && validateCliCallback(cliCallbackRaw)
      ? { url: cliCallbackRaw, state: cliState }
      : null;
  const retryParams = new URLSearchParams(searchParams.toString());
  retryParams.delete("error");
  if (cliCallbackRaw !== null && !validCliCallback) {
    retryParams.delete("cli_callback");
    retryParams.delete("cli_state");
  }
  const retryPath = retryParams.size
    ? `${paths.login()}?${retryParams.toString()}`
    : paths.root();

  if (!failed && !isLoading && user && validCliCallback) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background p-6">
        <Card className="w-full max-w-sm">
          <CardHeader className="text-center">
            <CardTitle className="text-title">
              {t(($) => $.cli.title)}
            </CardTitle>
            <CardDescription>
              {t(($) => $.cli.description, { email: user.email })}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <Button
              className="w-full"
              disabled={cliAuthorizing}
              onClick={() => {
                setCliAuthorizing(true);
                void api
                  .issueCliToken()
                  .then(({ token }) => {
                    redirectToCliCallback(
                      validCliCallback.url,
                      token,
                      validCliCallback.state,
                    );
                  })
                  .catch(() => {
                    setCliAuthorizing(false);
                    setHandoffFailed(true);
                  });
              }}
            >
              {cliAuthorizing
                ? t(($) => $.cli.authorizing)
                : t(($) => $.cli.authorize)}
            </Button>
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-background p-6">
      <Card className="w-full max-w-sm">
        <CardHeader className="text-center">
          <CardTitle className="text-title">
            {failed
              ? t(($) => $.web.enterprise.failed_title)
              : t(($) => $.web.enterprise.signing_in_title)}
          </CardTitle>
          <CardDescription>
            {failed
              ? t(($) => $.web.enterprise.failed_description)
              : t(($) => $.web.enterprise.signing_in_description)}
          </CardDescription>
        </CardHeader>
        <CardContent className="flex justify-center">
          {failed ? (
            <Button
              onClick={() => {
                window.location.href = retryPath;
              }}
            >
              <RefreshCw aria-hidden className="size-4" />
              {t(($) => $.web.enterprise.retry)}
            </Button>
          ) : (
            <Loader2
              aria-label={t(($) => $.web.enterprise.signing_in_title)}
              className="size-6 animate-spin text-muted-foreground motion-reduce:animate-none"
            />
          )}
        </CardContent>
      </Card>
    </div>
  );
}

export default function TMEOACallbackPage() {
  return (
    <Suspense fallback={null}>
      <TMEOACallbackContent />
    </Suspense>
  );
}
