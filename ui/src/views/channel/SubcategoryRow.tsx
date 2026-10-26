import { useState } from "react";
import { Button } from "../../ui";
import { listSubcategories, setChannelSubcategories } from "../../api/channels";
import type { Subcategory } from "../../api/types";

// The editor offers three slots: the cap the backend enforces
// (videos.MaxSubcategories). The first filled slot is the primary label.
const SLOTS = 3;

// SubcategoryRow is the channel header's subcategory line: what the channel is
// labelled as for Ask and MCP search, who set it, and an editor. The labels
// steer every one of the channel's videos in those searches — inbox reads
// included, which carry no labels of their own — so this is the one place
// they can be corrected.
export function SubcategoryRow({
  channelId,
  subcategories,
  manual,
  onSaved,
}: {
  channelId: string;
  subcategories: Subcategory[];
  manual: boolean;
  onSaved: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [vocab, setVocab] = useState<Subcategory[] | null>(null);
  const [picks, setPicks] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function startEdit() {
    setError(null);
    setPicks(
      Array.from({ length: SLOTS }, (_, i) => subcategories[i]?.id ?? ""),
    );
    setEditing(true);
    if (vocab) return;
    try {
      setVocab(await listSubcategories());
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function save(ids: string[]) {
    setBusy(true);
    setError(null);
    try {
      await setChannelSubcategories(channelId, ids);
      setEditing(false);
      onSaved();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  if (!editing) {
    return (
      <div className="chan-subcats" data-testid="chan-subcats">
        <span className="l">Subcategories</span>
        {subcategories.length ? (
          <span
            title={subcategories
              .map((s) => `${s.category_label} › ${s.label}`)
              .join("\n")}
          >
            {subcategories.map((s) => s.label).join(", ")}
          </span>
        ) : (
          <span className="faint">None yet</span>
        )}
        {subcategories.length > 0 && (
          <span className="faint">{manual ? "set by you" : "by peeq"}</span>
        )}
        <button type="button" className="link chan-more" onClick={startEdit}>
          Edit
        </button>
      </div>
    );
  }

  // Grouped by category, in vocabulary order, so a select reads like the
  // category list the rest of the app shows.
  const groups: { label: string; items: Subcategory[] }[] = [];
  for (const s of vocab ?? []) {
    const last = groups[groups.length - 1];
    if (last && last.label === s.category_label) last.items.push(s);
    else groups.push({ label: s.category_label, items: [s] });
  }
  const chosen = picks.filter(Boolean);
  const unique = new Set(chosen).size === chosen.length;

  return (
    <div className="chan-subcats editing" data-testid="chan-subcats">
      <span className="l">Subcategories</span>
      {picks.map((value, i) => (
        <select
          key={i}
          aria-label={i === 0 ? "Primary subcategory" : `Subcategory ${i + 1}`}
          value={value}
          disabled={!vocab || busy}
          onChange={(e) =>
            setPicks((p) => p.map((v, j) => (j === i ? e.target.value : v)))
          }
        >
          <option value="">—</option>
          {/* Until the vocabulary arrives, keep the current pick selectable
              so the select does not flash empty. */}
          {!vocab && value && <option value={value}>{value}</option>}
          {groups.map((g) => (
            <optgroup key={g.label} label={g.label}>
              {g.items.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.label}
                </option>
              ))}
            </optgroup>
          ))}
        </select>
      ))}
      <Button
        type="button"
        variant="primary"
        small
        busy={busy}
        disabled={!vocab || chosen.length === 0 || !unique}
        onClick={() => save(chosen)}
      >
        Save
      </Button>
      {/* An empty list is how the backend hands the channel back to the
          classifier; it is offered as its own action rather than as "save
          with every slot cleared", which reads like deleting the labels. */}
      {manual && (
        <Button
          type="button"
          variant="secondary"
          small
          busy={busy}
          onClick={() => save([])}
        >
          Let peeq decide
        </Button>
      )}
      <button
        type="button"
        className="link chan-more"
        disabled={busy}
        onClick={() => setEditing(false)}
      >
        Cancel
      </button>
      {!unique && <span className="errline">Each subcategory once.</span>}
      {error && <span className="errline">{error}</span>}
    </div>
  );
}
