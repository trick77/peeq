import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { IndexCard } from "./IndexCard";
import type { VideoEmbeddings } from "../../api/types";

function stats(overrides: Partial<VideoEmbeddings> = {}): VideoEmbeddings {
  return {
    model: "text-embedding-3-small",
    dimensions: 1536,
    chunks: 68,
    tokens: 38412,
    kinds: [
      { kind: "transcript", count: 54, tokens: 34000 },
      { kind: "chapter", count: 13, tokens: 4112 },
      { kind: "summary", count: 1, tokens: 300 },
    ],
    ...overrides,
  };
}

describe("IndexCard", () => {
  it("names every kind and totals them in the header", () => {
    render(<IndexCard stats={stats()} />);

    expect(screen.getByText("68 chunks")).toBeInTheDocument();
    expect(screen.getByText("Transcript")).toBeInTheDocument();
    expect(screen.getByText("Chapters")).toBeInTheDocument();
    expect(screen.getByText("Summary")).toBeInTheDocument();
    expect(screen.getByText("54")).toBeInTheDocument();
    // Grouped digits, and grouped the same way in every locale: the number
    // formatter is pinned, so a de-CH runner must not see 38’412.
    expect(screen.getByText("Tokens embedded")).toBeInTheDocument();
    expect(screen.getByText("38,412")).toBeInTheDocument();
    expect(screen.getByText("text-embedding-3-small")).toBeInTheDocument();
    expect(screen.getByText("1536 dimensions")).toBeInTheDocument();
  });

  // A kind the Go pipeline adds must still show up, under its own word, or the
  // rows would silently stop adding up to the header's total.
  it("shows an unknown kind rather than dropping it", () => {
    render(
      <IndexCard
        stats={stats({
          chunks: 2,
          kinds: [{ kind: "keyframe", count: 2, tokens: 10 }],
        })}
      />,
    );

    expect(screen.getByText("keyframe")).toBeInTheDocument();
    expect(screen.getByText("2 chunks")).toBeInTheDocument();
  });

  it("says so when nothing is indexed, and claims no model", () => {
    render(<IndexCard stats={{ chunks: 0, tokens: 0, kinds: [] }} />);

    expect(screen.getByText("Nothing indexed yet.")).toBeInTheDocument();
    expect(screen.queryByText(/chunks/)).not.toBeInTheDocument();
    expect(screen.queryByText(/dimensions/)).not.toBeInTheDocument();
  });

  it("uses the singular for a one-chunk index", () => {
    render(
      <IndexCard
        stats={stats({
          chunks: 1,
          kinds: [{ kind: "summary", count: 1, tokens: 300 }],
        })}
      />,
    );

    expect(screen.getByText("1 chunk")).toBeInTheDocument();
  });
});
