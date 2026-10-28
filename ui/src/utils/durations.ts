const units: [string, number][] = [
  ["d", 86400],
  ["h", 3600],
  ["m", 60],
  ["s", 1],
];

export function formatDuration(seconds: number): string {
  for (const [unit, size] of units) {
    if (seconds >= size && seconds % size === 0)
      return `${seconds / size}${unit}`;
  }
  return `${seconds}s`;
}
