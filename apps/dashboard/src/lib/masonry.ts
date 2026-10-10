// Packs cards of different heights into columns without the empty space a
// row-based grid leaves under short cards. Cards keep their order: each one
// goes, in turn, under the column that is shortest at that moment (the left
// one on a tie), so reading the cards top to bottom, left to right still
// follows that order. Afterwards only each column's last card stretches,
// so every column ends on the same line. Units are up to the caller (pixels
// or grid rows); the gap is the space between two cards in a column.

export type MasonryPlacement = {
  /** 0 is the left column. */
  column: number;
  /** Distance from the top of the grid to the card's top edge. */
  top: number;
  /**
   * The height the card takes: its own, or for a column's last card, its own
   * plus whatever brings its bottom level with the longest column.
   */
  span: number;
  /** The column's last card, the only one that stretches. */
  last: boolean;
};

export type MasonryLayout = {
  /** One placement per card, in the cards' order. */
  placements: MasonryPlacement[];
  /** Height of the longest column, where every column ends. */
  height: number;
};

/** Places cards of the given heights in `columns` columns, `gap` apart. */
export function packColumns(
  heights: number[],
  columns: number,
  gap: number,
): MasonryLayout {
  // An empty column ends one gap above the top, so its first card starts at
  // 0, and it is shorter than any column with a card.
  const bottoms = Array<number>(Math.max(1, columns)).fill(-gap);
  const lastInColumn = Array<number>(bottoms.length).fill(-1);
  const placements = heights.map((height, index) => {
    let column = 0;
    for (let candidate = 1; candidate < bottoms.length; candidate++) {
      if (bottoms[candidate] < bottoms[column]) column = candidate;
    }
    const top = bottoms[column] + gap;
    bottoms[column] = top + height;
    lastInColumn[column] = index;
    return { column, top, span: height, last: false };
  });
  const height = Math.max(0, ...bottoms);
  for (const index of lastInColumn) {
    if (index < 0) continue;
    placements[index].span = height - placements[index].top;
    placements[index].last = true;
  }
  return { placements, height };
}
