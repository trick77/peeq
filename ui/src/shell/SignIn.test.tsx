import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { SignIn } from "./SignIn";

describe("SignIn", () => {
  it("offers exactly one action, pointing at the login endpoint", () => {
    render(<SignIn />);
    const go = screen.getByRole("link", { name: "Sign in" });
    expect(go).toHaveAttribute("href", "/api/auth/login");
  });

  // The whole reason the old screen looked switched-off was the sign-in sitting
  // under the scrim. The card's stacking context is what keeps it above, and it
  // lives in CSS — so what this can assert is that the class carrying that rule
  // is present, and that the card is not a descendant of the covering image.
  it("puts the card outside the cover image", () => {
    const { container } = render(<SignIn />);
    const card = container.querySelector(".signin-card");
    const cover = container.querySelector("img.signin-cover");
    expect(card).toBeInTheDocument();
    expect(cover).toBeInTheDocument();
    expect(cover!.contains(card!)).toBe(false);
  });

  // The cover is decoration. A screen reader announcing it would be announcing
  // an abstract gradient.
  it("leaves the cover art out of the accessibility tree", () => {
    const { container } = render(<SignIn />);
    expect(container.querySelector("img.signin-cover")).toHaveAttribute(
      "alt",
      "",
    );
  });

  describe("while the session check is in flight", () => {
    it("spins instead of offering a button", () => {
      render(<SignIn checking />);
      expect(screen.queryByRole("link", { name: "Sign in" })).toBeNull();
      expect(screen.getByRole("status")).toHaveTextContent(
        "Checking your session",
      );
    });

    it("still renders the card, so resolving does not jolt the layout", () => {
      const { container } = render(<SignIn checking />);
      expect(container.querySelector(".signin-card")).toBeInTheDocument();
    });
  });

  describe("failures", () => {
    it("says the server is unreachable when getMe threw", () => {
      render(<SignIn unreachable />);
      expect(screen.getByRole("alert")).toHaveTextContent(
        "Couldn't reach the server",
      );
    });

    it("says sign-in did not complete after a failed callback", () => {
      render(<SignIn failed />);
      expect(screen.getByRole("alert")).toHaveTextContent(
        "Sign-in didn't complete",
      );
    });

    // Both at once is reachable: a callback fails, we bounce back with
    // ?auth_error, and the /api/auth/me that follows also cannot reach the
    // backend. Two alerts would contradict each other about what went wrong;
    // unreachable is the more fundamental fact and wins.
    it("shows only the unreachable message when both are set", () => {
      render(<SignIn unreachable failed />);
      const alerts = screen.getAllByRole("alert");
      expect(alerts).toHaveLength(1);
      expect(alerts[0]).toHaveTextContent("Couldn't reach the server");
    });

    it("still offers the action after a failure", () => {
      render(<SignIn failed />);
      expect(screen.getByRole("link", { name: "Sign in" })).toBeInTheDocument();
    });
  });
});
