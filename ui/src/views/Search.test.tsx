import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";
import { Search } from "./Search";

vi.mock("../api/search", () => ({
  searchVideos: vi.fn(),
}));

import { searchVideos } from "../api/search";

const mockedSearchVideos = vi.mocked(searchVideos);

describe("Search", () => {
  beforeEach(() => {
    mockedSearchVideos.mockReset();
  });

  it("shows results and navigates to a moment", async () => {
    mockedSearchVideos.mockResolvedValue([
      {
        video: { id: "v1", title: "iPhone 27 review" } as never,
        matches: [{ start_seconds: 560, snippet: "the new iPhone", distance: 0.1 }],
      },
    ]);
    const onOpen = vi.fn();
    render(<Search onOpen={onOpen} />);

    fireEvent.change(screen.getByPlaceholderText(/search/i), { target: { value: "iphone" } });
    fireEvent.submit(screen.getByRole("search"));

    expect(await screen.findByText("iPhone 27 review")).toBeInTheDocument();
    fireEvent.click(screen.getByText(/the new iPhone/));
    expect(onOpen).toHaveBeenCalledWith("v1", 560);
  });

  it("shows a hint state for a blank query", () => {
    render(<Search onOpen={vi.fn()} />);
    expect(mockedSearchVideos).not.toHaveBeenCalled();
    expect(screen.getByText(/search everything/i)).toBeInTheDocument();
  });

  it("calls searchVideos with the typed query on submit", async () => {
    mockedSearchVideos.mockResolvedValue([]);
    render(<Search onOpen={vi.fn()} />);
    fireEvent.change(screen.getByPlaceholderText(/search/i), { target: { value: "battery life" } });
    fireEvent.submit(screen.getByRole("search"));
    await waitFor(() => expect(mockedSearchVideos).toHaveBeenCalledWith("battery life"));
  });

  it("shows an empty-results message when a query returns nothing", async () => {
    mockedSearchVideos.mockResolvedValue([]);
    render(<Search onOpen={vi.fn()} />);
    fireEvent.change(screen.getByPlaceholderText(/search/i), { target: { value: "nothing here" } });
    fireEvent.submit(screen.getByRole("search"));
    expect(await screen.findByText(/no matches/i)).toBeInTheDocument();
  });

  it("clears stale results when a later search fails", async () => {
    mockedSearchVideos.mockResolvedValueOnce([
      {
        video: { id: "v1", title: "iPhone 27 review" } as never,
        matches: [{ start_seconds: 560, snippet: "the new iPhone", distance: 0.1 }],
      },
    ]);
    render(<Search onOpen={vi.fn()} />);

    fireEvent.change(screen.getByPlaceholderText(/search/i), { target: { value: "iphone" } });
    fireEvent.submit(screen.getByRole("search"));
    expect(await screen.findByText("iPhone 27 review")).toBeInTheDocument();

    mockedSearchVideos.mockRejectedValueOnce(new Error("search backend down"));
    fireEvent.change(screen.getByPlaceholderText(/search/i), { target: { value: "battery" } });
    fireEvent.submit(screen.getByRole("search"));

    expect(await screen.findByText(/search backend down/i)).toBeInTheDocument();
    expect(screen.queryByText("iPhone 27 review")).not.toBeInTheDocument();
  });
});
