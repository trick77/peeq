import type { User } from "./types";

// getMe returns the signed-in user, or null when there is no session (a 401
// here means "signed out", not "session expired mid-use" — the caller is
// bootstrapping, so this resolves rather than throwing AuthExpiredError).
export async function getMe(): Promise<User | null> {
  const response = await fetch("/api/auth/me");
  if (response.status === 401) {
    return null;
  }
  if (!response.ok) {
    throw new Error("failed to load current user");
  }
  return response.json() as Promise<User>;
}
