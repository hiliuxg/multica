import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

const {
  authState,
  workspaceState,
  searchParamsState,
  mockReplace,
  mockIssueCliToken,
  mockRedirectToCliCallback,
} = vi.hoisted(() => ({
  authState: {
    user: null as null | {
      id: string;
      email: string;
      onboarded_at: string | null;
    },
    isLoading: false,
  },
  workspaceState: {
    workspaces: [] as Array<{ id: string; slug: string }>,
    ready: true,
    unavailable: false,
  },
  searchParamsState: { value: new URLSearchParams() },
  mockReplace: vi.fn(),
  mockIssueCliToken: vi.fn(),
  mockRedirectToCliCallback: vi.fn(),
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: mockReplace }),
  useSearchParams: () => searchParamsState.value,
}));

vi.mock("@multica/core/auth", async () => {
  const actual =
    await vi.importActual<typeof import("@multica/core/auth")>(
      "@multica/core/auth",
    );
  const useAuthStore = (selector: (state: typeof authState) => unknown) =>
    selector(authState);
  return { ...actual, useAuthStore };
});

vi.mock("@multica/core/api", () => ({
  api: { issueCliToken: mockIssueCliToken },
}));

vi.mock("@multica/core/workspace", () => ({
  useWorkspaceList: () => workspaceState,
}));

vi.mock("@multica/views/auth", () => ({
  validateCliCallback: (value: string) => value.startsWith("http://localhost:"),
  redirectToCliCallback: mockRedirectToCliCallback,
}));

vi.mock("@multica/views/i18n", () => ({
  useT: () => ({ t: () => "Enterprise sign-in" }),
}));

import TMEOACallbackPage from "./page";

describe("TMEOACallbackPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    authState.user = null;
    authState.isLoading = false;
    workspaceState.workspaces = [];
    workspaceState.ready = true;
    workspaceState.unavailable = false;
    searchParamsState.value = new URLSearchParams();
    mockIssueCliToken.mockResolvedValue({ token: "cli-token" });
  });

  it("sends a new enterprise user to onboarding", async () => {
    authState.user = {
      id: "user-new",
      email: "new@tencentmusic.com",
      onboarded_at: null,
    };

    render(<TMEOACallbackPage />);

    await waitFor(() => {
      expect(mockReplace).toHaveBeenCalledWith("/onboarding");
    });
  });

  it("does not let a next URL bypass onboarding for a new user", async () => {
    searchParamsState.value = new URLSearchParams({ next: "/invite/abc" });
    authState.user = {
      id: "user-new",
      email: "new@tencentmusic.com",
      onboarded_at: null,
    };

    render(<TMEOACallbackPage />);

    await waitFor(() => {
      expect(mockReplace).toHaveBeenCalledWith("/onboarding");
    });
  });

  it("sends an onboarded user to their existing workspace", async () => {
    authState.user = {
      id: "user-existing",
      email: "existing@tencentmusic.com",
      onboarded_at: "2026-08-01T00:00:00Z",
    };
    workspaceState.workspaces = [{ id: "workspace-1", slug: "acme" }];

    render(<TMEOACallbackPage />);

    await waitFor(() => {
      expect(mockReplace).toHaveBeenCalledWith("/acme/issues");
    });
  });

  it("restores a safe next URL for an onboarded user", async () => {
    searchParamsState.value = new URLSearchParams({ next: "/invite/abc" });
    authState.user = {
      id: "user-existing",
      email: "existing@tencentmusic.com",
      onboarded_at: "2026-08-01T00:00:00Z",
    };

    render(<TMEOACallbackPage />);

    await waitFor(() => {
      expect(mockReplace).toHaveBeenCalledWith("/invite/abc");
    });
  });

  it("keeps CLI confirmation when desktop and CLI parameters are mixed", async () => {
    searchParamsState.value = new URLSearchParams({
      platform: "desktop",
      cli_callback: "http://localhost:9876/callback",
      cli_state: "state-1",
    });
    authState.user = {
      id: "user-existing",
      email: "existing@tencentmusic.com",
      onboarded_at: "2026-08-01T00:00:00Z",
    };

    render(<TMEOACallbackPage />);

    expect(mockIssueCliToken).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button"));
    await waitFor(() => {
      expect(mockRedirectToCliCallback).toHaveBeenCalledWith(
        "http://localhost:9876/callback",
        "cli-token",
        "state-1",
      );
    });
  });
});
