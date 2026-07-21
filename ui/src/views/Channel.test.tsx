import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { Channel } from "./Channel";

describe("Channel routing", () => {
  it("renders the selected channel's id", () => {
    render(<Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />);
    expect(screen.getByTestId("channel-page")).toHaveTextContent("UCa");
  });

  it("says so when no channel is selected", () => {
    render(<Channel channelId={null} onOpenVideo={() => {}} onBack={() => {}} />);
    expect(screen.getByText(/no channel selected/i)).toBeInTheDocument();
  });
});
